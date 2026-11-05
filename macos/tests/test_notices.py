import ast
import json
import os
import pathlib
import tempfile
import unittest


script = pathlib.Path(__file__).resolve().parents[1] / "scripts/package.sh"
blocks = (block.split("\nPY\n", 1)[0] for block in script.read_text().split("<<'PY'\n")[1:])
body = ast.parse(next(block for block in blocks if "def copy_qt_notices(" in block))
body.body = [node for node in body.body if isinstance(node, (ast.Import, ast.FunctionDef))]
notices = {}
exec(compile(body, str(script), "exec"), notices)
copy_qt_notices = notices["copy_qt_notices"]


class NoticeTests(unittest.TestCase):
    def test_copy_and_reject_incomplete_notices(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory) / "source"
            root.mkdir()
            target = pathlib.Path(directory) / "notices"
            expected = {"LICENSE": b"module", "extra-zlib.txt": b"zlib", "extra-ijg.txt": b"jpeg"}
            for name, content in expected.items():
                (root / name).write_bytes(content)
            attribution = root / "qt_attribution.json"
            metadata = [{"Description": "raw\ncontrol", "LicenseFile": "extra-zlib.txt"},
                        {"LicenseFiles": ["extra-ijg.txt"]}]
            attribution.write_text(json.dumps(metadata).replace(r"\n", "\n"))
            expected[attribution.name] = attribution.read_bytes()
            copy_qt_notices(root, target)
            self.assertEqual({path.name: path.read_bytes() for path in target.iterdir()}, expected)

            (root / "LICENSE").chmod(0o444)
            copy_qt_notices(root, target)
            expected["extra-zlib.txt"] = b"updated zlib notice"
            (root / "extra-zlib.txt").write_bytes(expected["extra-zlib.txt"])
            copy_qt_notices(root, target)
            self.assertEqual({path.name: path.read_bytes() for path in target.iterdir()}, expected)

            (root.parent / "outside.txt").write_text("outside")
            for value in ("{", "null", '{"LicenseFile":"missing.txt"}', '{"LicenseFile":"../outside.txt"}',
                          '{"LicenseFiles":[]}', '{"LicenseFiles":["extra-ijg.txt", null]}'):
                with self.subTest(metadata=value):
                    attribution.write_text(value)
                    rejected = target / "rejected"
                    with self.assertRaisesRegex(ValueError, "qt_attribution.json"):
                        copy_qt_notices(root, rejected)
                    self.assertFalse(rejected.exists())

    @unittest.skipUnless(os.environ.get("QT_SOURCE_DIR"), "Set QT_SOURCE_DIR to check real Qt sources")
    def test_verified_qt_sources(self):
        source = pathlib.Path(os.environ["QT_SOURCE_DIR"]).resolve()
        required = (
            "qtbase/src/3rdparty/freetype/ZLIB-LICENSE.txt",
            "qtbase/src/3rdparty/freetype/BDF-LICENSE.txt",
            "qtbase/src/3rdparty/freetype/PCF-LICENSE.txt",
            "qtbase/src/3rdparty/libjpeg/ijg-license.txt",
            "qtbase/src/3rdparty/libpsl/PSL-LICENSE.txt",
            "qtbase/src/3rdparty/sha3/BRG_ENDIAN_LICENSE",
            "qtbase/src/3rdparty/sha3/CC0_LICENSE",
        )
        with tempfile.TemporaryDirectory() as directory:
            target = pathlib.Path(directory)
            for module in ("qtbase", "qtshadertools", "qtdeclarative", "qtsvg", "qt5compat", "qtimageformats"):
                self.assertTrue((source / module).is_dir(), module)
                copy_qt_notices(source / module, target / module)
                for attribution in (source / module).rglob("qt_attribution.json"):
                    entries = json.loads(attribution.read_text(), strict=False)
                    for entry in entries if isinstance(entries, list) else [entries]:
                        names = entry.get("LicenseFiles", [entry["LicenseFile"]] if "LicenseFile" in entry else [])
                        for name in names:
                            original = (attribution.parent / name).resolve()
                            self.assertEqual((target / original.relative_to(source)).read_bytes(), original.read_bytes())
            for relative in required:
                self.assertEqual((target / relative).read_bytes(), (source / relative).read_bytes())
                print(f"Copied {relative}")
            print(f"Copied {sum(path.is_file() for path in target.rglob('*'))} Qt notice files")


if __name__ == "__main__":
    unittest.main()
