"""Exercise the bundled native helper without changing system state."""

import json
import pathlib
import subprocess
import sys
import unittest


BINARY = pathlib.Path(sys.argv.pop(1)).resolve() if len(sys.argv) > 1 and pathlib.Path(sys.argv[1]).is_file() else None


@unittest.skipUnless(BINARY and BINARY.is_file(), "Pass the built native helper executable")
class NativeHelperTests(unittest.TestCase):
    def invoke(self, request):
        return subprocess.run([str(BINARY)], input=request, capture_output=True, timeout=10)

    def test_invalid_input_is_rejected(self):
        for payload in (b"invalid", b"[]", b'{}', b'{"method":"apps.query","params":[]}'):
            with self.subTest(payload=payload):
                result = self.invoke(payload)
                self.assertNotEqual(result.returncode, 0)
                response = json.loads(result.stdout)
                self.assertEqual(response["code"], "EINVAL")
                self.assertTrue(response["error"])

    def test_unknown_method_is_rejected(self):
        result = self.invoke(b'{"method":"fixture.unknown","params":{}}')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(json.loads(result.stdout)["code"], "NOTSUPPORTED")

    def test_missing_media_file_fails_without_output(self):
        result = self.invoke(b'{"method":"media.thumbnail","params":{"path":"/no/such/dux-file.pdf","size":128}}')
        self.assertNotEqual(result.returncode, 0)
        response = json.loads(result.stdout)
        self.assertNotIn("data", response)
        self.assertTrue(response["error"])


if __name__ == "__main__":
    unittest.main()
