import unittest

import edge_overhead as contract


class OverheadContractTests(unittest.TestCase):
    def pairs(self, overhead=2, token_mismatch=False):
        return [{'direct': {'elapsed_ms': 1000, 'compute_ms': 990, 'output_tokens': 50},
                 'edge': {'elapsed_ms': 1000 + overhead, 'compute_ms': 990,
                          'output_tokens': 49 if token_mismatch else 50}} for _ in range(30)]

    def test_clean_pair_passes(self):
        report = contract.summarize(self.pairs())
        self.assertTrue(report['passed'])
        self.assertEqual(report['p95_edge_overhead_ms'], 2)

    def test_regression_and_mismatch_fail(self):
        self.assertFalse(contract.summarize(self.pairs(overhead=60))['passed'])
        self.assertFalse(contract.summarize(self.pairs(token_mismatch=True))['passed'])
        with self.assertRaises(ValueError):
            contract.summarize(self.pairs()[:3])

    def test_runtime_latency_is_not_edge_overhead(self):
        pairs = self.pairs()
        for pair in pairs:
            pair['edge']['elapsed_ms'] += 200
            pair['edge']['compute_ms'] += 200
        report = contract.summarize(pairs)
        self.assertEqual(report['p95_edge_overhead_ms'], 2)
        self.assertFalse(report['passed'])


if __name__ == '__main__':
    unittest.main()
