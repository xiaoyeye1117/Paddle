import paddle.v2.framework.layers as layers
import paddle.v2.framework.nets as nets
from paddle.v2.framework.framework import Program, g_program
import paddle.v2.framework.core as core
import unittest


class TestBook(unittest.TestCase):
    def test_fit_a_line(self):
        program = Program()
        x = layers.data(
            name='x', shape=[13], data_type='float32', program=program)
        y_predict = layers.fc(input=x, size=1, act=None, program=program)

        y = layers.data(
            name='y', shape=[1], data_type='float32', program=program)
        cost = layers.square_error_cost(
            input=y_predict, label=y, program=program)

        avg_cost = layers.mean(x=cost, program=program)
        self.assertIsNotNone(avg_cost)
        program.append_backward(avg_cost)
        print str(program)

    def test_recognize_digits_mlp(self):
        program = Program()

        # Change g_program, so the rest layers use `g_program`
        images = layers.data(
            name='pixel', shape=[784], data_type='float32', program=program)
        label = layers.data(
            name='label', shape=[1], data_type='int32', program=program)
        hidden1 = layers.fc(input=images, size=128, act='relu', program=program)
        hidden2 = layers.fc(input=hidden1, size=64, act='relu', program=program)
        predict = layers.fc(input=hidden2,
                            size=10,
                            act='softmax',
                            program=program)
        cost = layers.cross_entropy(input=predict, label=label, program=program)
        avg_cost = layers.mean(x=cost, program=program)
        self.assertIsNotNone(avg_cost)
        print str(program)

    def test_simple_conv2d(self):
        program = Program()
        images = layers.data(
            name='pixel', shape=[3, 48, 48], data_type='int32', program=program)
        layers.conv2d(
            input=images, num_filters=3, filter_size=[4, 4], program=program)

        print str(program)

    def test_recognize_digits_conv(self):
        program = Program()

        images = layers.data(
            name='pixel',
            shape=[1, 28, 28],
            data_type='float32',
            program=program)
        label = layers.data(
            name='label', shape=[1], data_type='int32', program=program)
        conv_pool_1 = nets.simple_img_conv_pool(
            input=images,
            filter_size=5,
            num_filters=2,
            pool_size=2,
            pool_stride=2,
            act="relu",
            program=program)
        conv_pool_2 = nets.simple_img_conv_pool(
            input=conv_pool_1,
            filter_size=5,
            num_filters=4,
            pool_size=2,
            pool_stride=2,
            act="relu",
            program=program)

        predict = layers.fc(input=conv_pool_2,
                            size=10,
                            act="softmax",
                            program=program)
        cost = layers.cross_entropy(input=predict, label=label, program=program)
        avg_cost = layers.mean(x=cost, program=program)

        program.append_backward(avg_cost)

        print str(program)

    def test_word_embedding(self):
        program = Program()
        dict_size = 10000
        embed_size = 32
        first_word = layers.data(
            name='firstw', shape=[1], data_type='int32', program=program)
        second_word = layers.data(
            name='secondw', shape=[1], data_type='int32', program=program)
        third_word = layers.data(
            name='thirdw', shape=[1], data_type='int32', program=program)
        forth_word = layers.data(
            name='forthw', shape=[1], data_type='int32', program=program)
        next_word = layers.data(
            name='nextw', shape=[1], data_type='int32', program=program)

        embed_param_attr_1 = {
            'name': 'shared_w',
            'init_attr': {
                'max': 1.0,
                'type': 'uniform_random',
                'min': -1.0
            }
        }
        embed_param_attr_2 = {'name': 'shared_w'}

        embed_first = layers.embedding(
            input=first_word,
            size=[dict_size, embed_size],
            data_type='float32',
            param_attr=embed_param_attr_1,
            program=program)
        embed_second = layers.embedding(
            input=second_word,
            size=[dict_size, embed_size],
            data_type='float32',
            param_attr=embed_param_attr_2,
            program=program)

        embed_third = layers.embedding(
            input=third_word,
            size=[dict_size, embed_size],
            data_type='float32',
            param_attr=embed_param_attr_2,
            program=program)
        embed_forth = layers.embedding(
            input=forth_word,
            size=[dict_size, embed_size],
            data_type='float32',
            param_attr=embed_param_attr_2,
            program=program)

        concat_embed = layers.concat(
            input=[embed_first, embed_second, embed_third, embed_forth],
            axis=1,
            program=program)

        hidden1 = layers.fc(input=concat_embed,
                            size=256,
                            act='sigmoid',
                            program=program)
        predict_word = layers.fc(input=hidden1,
                                 size=dict_size,
                                 act='softmax',
                                 program=program)
        cost = layers.cross_entropy(
            input=predict_word, label=next_word, program=program)
        avg_cost = layers.mean(x=cost, program=program)
        self.assertIsNotNone(avg_cost)

        print str(program)


if __name__ == '__main__':
    unittest.main()
