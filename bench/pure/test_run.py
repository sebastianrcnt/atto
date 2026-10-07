import unittest
from run import correct


class CheckerTest(unittest.TestCase):
    def test_exact(self):
        problem = {"checker": "exact", "expected": "123"}
        self.assertTrue(correct(problem, " 123\n"))
        self.assertFalse(correct(problem, "answer: 123"))

    def test_regexp(self):
        problem = {"checker": "regexp", "expected": r"[0-9]+"}
        self.assertTrue(correct(problem, " 123\n"))
        self.assertFalse(correct(problem, "x123"))

    def test_unknown(self):
        with self.assertRaises(ValueError):
            correct({"checker": "bad"}, "")


if __name__ == "__main__":
    unittest.main()
