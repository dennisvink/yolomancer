import ast
import importlib.util
import io
import json
from pathlib import Path
import unittest
from unittest.mock import Mock, patch

PATH = Path(__file__).parents[1] / "case_law_search.py"
spec = importlib.util.spec_from_file_location("case_law_search", PATH)
tool = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tool)


def stream(value):
    return io.BytesIO(json.dumps(value).encode())


class SearchTests(unittest.TestCase):
    def clients(self, count=1, text="Bronpassage"):
        bedrock, vectors, s3 = Mock(), Mock(), Mock()
        bedrock.invoke_model.return_value = {"body": stream({"embedding": [0.1] * 1024})}
        vectors.query_vectors.return_value = {"vectors": [
            {"metadata": {"processed_key": f"chunks/doc/{i}.json", "ecli": "ECLI:NL:TEST:2026:1", "court": "Test court"}, "distance": 0.2}
            for i in range(count)
        ]}
        s3.get_object.side_effect = lambda **kw: {"Body": stream({"text": text, "chunk_id": kw["Key"]})}
        return bedrock, vectors, s3

    def test_schema_discoverable_without_imports(self):
        function = next(n for n in ast.parse(PATH.read_text()).body if isinstance(n, ast.FunctionDef) and n.name == "yolomancer_tool")
        namespace = {}
        exec(compile(ast.Module(body=[function], type_ignores=[]), str(PATH), "exec"), namespace)
        self.assertEqual(namespace["yolomancer_tool"]()["name"], "case_law_search")

    def test_search_and_filters(self):
        clients = self.clients()
        with patch.object(tool, "_clients", return_value=clients):
            result = tool.run({"query": " verzekering "})
        self.assertTrue(result["ok"])
        self.assertEqual(result["query"], "verzekering")
        self.assertEqual(result["passages"][0]["text"], "Bronpassage")
        self.assertIn("ECLI:NL:TEST:2026:1", result["passages"][0]["citation_url"])
        request = clients[1].query_vectors.call_args.kwargs
        self.assertEqual(request["topK"], 5)
        self.assertEqual(len(request["filter"]["$and"]), 3)
        self.assertEqual(json.loads(clients[0].invoke_model.call_args.kwargs["body"])["dimensions"], 1024)
        json.dumps(result, allow_nan=False)

    def test_validation_before_aws(self):
        with patch.object(tool, "_clients") as clients:
            for args in (None, {}, {"query": " "}, {"query": "x" * 4001}, {"query": "q", "limit": True}, {"query": "q", "limit": 11}, {"query": "q", "bucket": "elsewhere"}):
                self.assertFalse(tool.run(args)["ok"])
            clients.assert_not_called()

    def test_bounds(self):
        result = tool._search("q", 10, self.clients(10, "x" * 9000))
        self.assertLessEqual(sum(len(p["text"]) for p in result["passages"]), tool.MAX_TOTAL_CHARS)
        self.assertTrue(all(p["truncated"] for p in result["passages"]))
        self.assertTrue(result["incomplete"])

    def test_missing_sources_and_duplicates(self):
        clients = self.clients()
        hits = clients[1].query_vectors.return_value["vectors"]
        hits.extend([hits[0], {"metadata": {"processed_key": "private/secret.json"}}])
        result = tool._search("q", 5, clients)
        self.assertEqual(result["count"], 1)
        self.assertTrue(result["incomplete"])
        self.assertEqual(clients[2].get_object.call_count, 1)

    def test_failed_source_not_silent_empty_success(self):
        clients = self.clients()
        clients[2].get_object.side_effect = RuntimeError("sensitive detail")
        result = tool._search("q", 5, clients)
        self.assertFalse(result["ok"])
        self.assertNotIn("sensitive detail", json.dumps(result))

    def test_empty_matches(self):
        self.assertTrue(tool._search("q", 5, self.clients(0))["ok"])

    def test_bad_embedding(self):
        clients = self.clients()
        clients[0].invoke_model.return_value = {"body": stream({"embedding": [0.1]})}
        with self.assertRaises(ValueError):
            tool._search("q", 5, clients)
        clients[1].query_vectors.assert_not_called()

    def test_oversize_stream_closed(self):
        body = io.BytesIO(b"x" * 20)
        with self.assertRaises(ValueError):
            tool._read_json(body, 10)
        self.assertTrue(body.closed)

    def test_error_redaction(self):
        with patch.object(tool, "_clients", side_effect=RuntimeError("SECRET")):
            self.assertNotIn("SECRET", json.dumps(tool.run({"query": "q"})))


if __name__ == "__main__":
    unittest.main()
