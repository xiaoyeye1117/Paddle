// Copyright (c) 2016 PaddlePaddle Authors. All Rights Reserve.

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

// http://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pserver

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strconv"
	"sync"
	"time"

	uuid "github.com/satori/go.uuid"

	log "github.com/sirupsen/logrus"
)

// ElementType is the type of elements of a Parameter.
type ElementType int

// ErrCheckpointNotFound indicates that the pserver checkpoint could
// not be found.
var ErrCheckpointNotFound = errors.New("checkpoint not found")

// RPC error message.
const (
	AlreadyInitialized = "pserver already initialized"
	Uninitialized      = "pserver not fully initialized"
	WrongChecksum      = "checkpoint file checksum validation failed"
)

// Supported element types.
const (
	Int32 ElementType = iota
	UInt32
	Int64
	UInt64
	Float32
	Float64
)

// Parameter is a piece of data to sync with the parameter server.
type Parameter struct {
	Name        string
	ElementType ElementType
	Content     []byte
}

// ParameterWithConfig contains the parameter and the configuration.
type ParameterWithConfig struct {
	Param  Parameter
	Config []byte // parameter configuration in Proto Buffer format
}

// checkpointMeta saves checkpoint metadata
type checkpointMeta struct {
	UUID      string `json:"uuid"`
	Path      string `json:"path"`
	MD5       string `json:"md5"`
	Timestamp int64  `json:"timestamp"`
}

// Checkpoint is the pserver shard persist in file.
type Checkpoint []parameterCheckpoint

// Gradient is the gradient of the parameter.
type Gradient Parameter

// Service is the RPC service for pserver.
type Service struct {
	initialized        chan struct{}
	idx                int
	checkpointInterval time.Duration
	checkpointPath     string
	client             *EtcdClient

	mu     sync.Mutex
	optMap map[string]*optimizer
}

// parameterCheckpoint saves parameter checkpoint.
type parameterCheckpoint struct {
	ParameterWithConfig
	State []byte
}

func loadMeta(e *EtcdClient, idx int) (meta checkpointMeta, err error) {
	v, err := e.GetKey(PsCheckpoint+strconv.Itoa(idx), 3*time.Second)
	if err != nil {
		return
	}

	if len(v) == 0 {
		err = ErrCheckpointNotFound
		return
	}

	if err = json.Unmarshal(v, &meta); err != nil {
		return
	}

	return
}

// LoadCheckpoint loads checkpoint from file.
func LoadCheckpoint(e *EtcdClient, idx int) (Checkpoint, error) {
	cpMeta, err := loadMeta(e, idx)
	if err != nil {
		return nil, err
	}

	content, err := ioutil.ReadFile(cpMeta.Path)
	if err != nil {
		return nil, err
	}

	// TODO(helin): change MD5 to CRC since CRC is better for file
	// checksum in our use case (emphasize speed over security).
	h := md5.New()
	md5 := hex.EncodeToString(h.Sum(content))
	if md5 != cpMeta.MD5 {
		return nil, errors.New(WrongChecksum)
	}

	dec := gob.NewDecoder(bytes.NewReader(content))
	var cp Checkpoint
	if err = dec.Decode(&cp); err != nil {
		return nil, err
	}
	return cp, nil
}

// NewService creates a new service, will bypass etcd registration if no
// endpoints specified. It will recovery from checkpoint file if a exists a specified checkpoint.
func NewService(idx int, interval time.Duration, path string, client *EtcdClient, cp Checkpoint) (*Service, error) {
	s := &Service{
		idx:                idx,
		checkpointInterval: interval,
		checkpointPath:     path,
		client:             client,
	}
	s.optMap = make(map[string]*optimizer)
	s.initialized = make(chan struct{})

	if cp != nil {
		for _, item := range cp {
			p := ParameterWithConfig{
				Param:  item.Param,
				Config: item.Config,
			}
			s.optMap[p.Param.Name] = newOptimizer(p, item.State)
		}
	}
	return s, nil
}

// InitParam initializes a parameter.
func (s *Service) InitParam(paramWithConfigs ParameterWithConfig, _ *int) error {
	select {
	case <-s.initialized:
		return errors.New(AlreadyInitialized)
	default:
	}

	// TODO(helin): parse parameter config

	s.mu.Lock()
	defer s.mu.Unlock()

	// TODO(helin): check if paramWithConfigs.Param.Content is
	// properly memory aligned, if not, make copy to a memory
	// aligned region.
	s.optMap[paramWithConfigs.Param.Name] = newOptimizer(paramWithConfigs, nil)
	return nil
}

// FinishInitParams tells the parameter server that the parameter
// initialization has finished.
func (s *Service) FinishInitParams(_ int, _ *int) error {
	select {
	case <-s.initialized:
		return errors.New(AlreadyInitialized)
	default:
	}

	close(s.initialized)
	go func() {
		t := time.Tick(s.checkpointInterval)
		for range t {
			err := s.checkpoint()
			if err != nil {
				log.Errorln(err)
			}
		}
	}()
	return nil
}

// SendGrad sends gradient to parameter servers for parameter
// optimization.
func (s *Service) SendGrad(g Gradient, _ *int) error {
	select {
	case <-s.initialized:
	default:
		return errors.New(Uninitialized)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	o, ok := s.optMap[g.Name]
	if !ok {
		return fmt.Errorf("parameter: %s does not exist", g.Name)
	}

	return o.UpdateParameter(g)
}

// GetParam gets parameters from the parameter server.
func (s *Service) GetParam(name string, parameter *Parameter) error {
	<-s.initialized
	s.mu.Lock()
	defer s.mu.Unlock()

	opt, ok := s.optMap[name]
	if !ok {
		return fmt.Errorf("parameter: %s does not exist", name)
	}

	// The parameter content (a byte slice) may change
	// during RPC serialization due to write from other
	// goroutine, we allow it since mini-batch based deep
	// learning optimization methods are stochastic in
	// nature. This race condition is allowed deliberately
	// to save the program from making a copy of the
	// parameter content.
	parameter.Name = name
	parameter.ElementType = opt.elementType
	parameter.Content = opt.GetWeights()
	return nil
}

func traceTime(start time.Time, name string) {
	elapsed := time.Since(start)
	log.Infof("%s took %v", name, elapsed)
}

// checkpoint saves checkpoint to disk.
//
// checkpoint should be only called after the parameters are
// initialized.
func (s *Service) checkpoint() (err error) {
	log.Infoln("Begin save checkpoint.")
	defer traceTime(time.Now(), "save checkpoint")

	s.mu.Lock()
	cp := make([]parameterCheckpoint, len(s.optMap))
	index := 0
	// TODO(helin): write checkpoint incrementally to reduce memory
	// footprint during checkpoint.
	for name, opt := range s.optMap {
		var pc parameterCheckpoint
		pc.Param.Name = name
		pc.Param.ElementType = opt.elementType
		pc.Param.Content = opt.GetWeights()
		pc.Config = opt.config
		pc.State = opt.GetStates()
		cp[index] = pc
		index++
	}
	s.mu.Unlock()

	var buf bytes.Buffer
	encoder := gob.NewEncoder(&buf)
	err = encoder.Encode(cp)
	if err != nil {
		return
	}

	if _, err = os.Stat(s.checkpointPath); os.IsNotExist(err) {
		err = os.MkdirAll(s.checkpointPath, os.ModePerm)
		if err != nil {
			return
		}
	}

	id := uuid.NewV4().String()
	p := path.Join(s.checkpointPath, id)
	f, err := os.Create(p)
	if err != nil {
		return
	}

	defer func() {
		closeErr := f.Close()
		if closeErr != nil {
			if err != nil {
				log.Errorln(closeErr)
			} else {
				// Set closeErr as return value.
				err = closeErr
			}
		}
	}()

	writer := bufio.NewWriter(f)
	_, err = writer.Write(buf.Bytes())
	if err != nil {
		return
	}

	err = writer.Flush()
	if err != nil {
		return
	}

	oldMeta, err := loadMeta(s.client, s.idx)
	if err == ErrCheckpointNotFound {
		log.Infoln("Do not have existing checkpoint.")
		err = nil
	}

	if err != nil {
		return
	}

	h := md5.New()
	md5 := hex.EncodeToString(h.Sum(buf.Bytes()))
	cpMeta := checkpointMeta{
		UUID:      id,
		Timestamp: time.Now().UnixNano(),
		MD5:       md5,
		Path:      p,
	}

	json, err := json.Marshal(cpMeta)
	if err != nil {
		return
	}

	err = s.client.PutKey(PsCheckpoint+strconv.Itoa(s.idx), json, 3*time.Second, false)
	if err != nil {
		return
	}

	if oldMeta.Path != "" {
		rmErr := os.Remove(oldMeta.Path)
		if rmErr != nil {
			// log error, but still treat checkpoint as
			// successful.
			log.Errorln(rmErr)
		}
	}

	return
}
