import importlib.util
import pathlib
import plistlib
import sys
import tempfile
import unittest
from unittest.mock import patch


script = pathlib.Path(__file__).resolve().parents[1] / "scripts/check-dependencies.py"
spec = importlib.util.spec_from_file_location("dependencies", script)
if spec is None or spec.loader is None:
    raise RuntimeError(f"Cannot import dependency checker: {script}")
dependencies = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = dependencies
spec.loader.exec_module(dependencies)


class DependencyTests(unittest.TestCase):
    def test_apple_system_paths(self):
        for path in (
            "/usr/lib/libSystem.B.dylib",
            "/usr/lib/swift/libswiftCore.dylib",
            "/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit",
            "/System/Library/PrivateFrameworks/CoreUI.framework/Versions/A/CoreUI",
            "/System/Volumes/Preboot/Cryptexes/OS/usr/lib/libobjc.A.dylib",
        ):
            with self.subTest(path=path):
                self.assertEqual(dependencies.classify_dependency(path), "system")

    def test_third_party_paths(self):
        for path, classification in (
            ("@rpath/QtCore.framework/Versions/A/QtCore", "loader-relative"),
            ("@loader_path/../Frameworks/libfoo.dylib", "loader-relative"),
            ("@executable_path/libfoo.dylib", "loader-relative"),
            ("/opt/homebrew/opt/qt/lib/QtCore.framework/QtCore", "package-manager"),
            ("/usr/local/Cellar/qt/lib/libQt6Core.dylib", "package-manager"),
            ("/Users/test/build/qt/lib/libQt6Core.dylib", "build-tree"),
            ("/Library/Frameworks/QtCore.framework/QtCore", "non-system"),
            ("relative/libfoo.dylib", "invalid"),
            ("/System/Library/../../opt/homebrew/lib/libfoo.dylib", "package-manager"),
        ):
            with self.subTest(path=path):
                self.assertEqual(dependencies.classify_dependency(path), classification)

    def test_parse_universal_otool_with_spaces(self):
        text = """/tmp/Dux Files.app/Contents/MacOS/Dux Files (architecture arm64):
\t/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit (compatibility version 45.0.0, current version 2566.0.0)
\t@rpath/Qt Core.framework/Versions/A/Qt Core (compatibility version 6.0.0, current version 6.10.0)
/tmp/Dux Files.app/Contents/MacOS/Dux Files (architecture x86_64):
\t/usr/lib/libSystem.B.dylib (compatibility version 1.0.0, current version 1351.0.0)
"""
        result = dependencies.parse_otool(text)
        self.assertEqual([item.classification for item in result], ["system", "loader-relative", "system"])
        self.assertEqual(result[1].path, "@rpath/Qt Core.framework/Versions/A/Qt Core")

    def test_malformed_otool_is_an_error(self):
        for output in (
            "not otool output",
            "/tmp/app:\n\t@rpath/libHidden.dylib unexpected output\n",
            "/tmp/app:\n\t@rpath/libHidden.dylib:\n",
            "\t/usr/lib/libSystem.B.dylib (compatibility version 1.0.0, current version 1.0.0)\n",
        ):
            with self.subTest(output=output), self.assertRaises(ValueError):
                dependencies.parse_otool(output)

    def test_universal_macho_types_and_invalid_headers(self):
        output = """/tmp/app (architecture arm64):
Mach header
      magic cputype cpusubtype caps filetype ncmds sizeofcmds flags
MH_MAGIC_64 ARM64 ALL 0x00 EXECUTE 20 1712 NOUNDEFS
/tmp/app (architecture x86_64):
Mach header
      magic cputype cpusubtype caps filetype ncmds sizeofcmds flags
MH_MAGIC_64 X86_64 ALL 0x00 DYLIB 20 1712 NOUNDEFS
"""
        result = dependencies.subprocess.CompletedProcess([], 0, output, "")
        with patch.object(dependencies.subprocess, "run", return_value=result):
            self.assertEqual(dependencies.inspect_types(pathlib.Path("/tmp/app")), {"EXECUTE", "DYLIB"})
        for output in ("not a Mach-O header", "MH_MAGIC_64 ARM64\n"):
            result = dependencies.subprocess.CompletedProcess([], 0, output, "")
            with self.subTest(output=output), patch.object(dependencies.subprocess, "run", return_value=result):
                with self.assertRaises(ValueError):
                    dependencies.inspect_types(pathlib.Path("/tmp/app"))

    def test_every_embedded_macho_is_checked(self):
        with tempfile.TemporaryDirectory() as directory:
            app = pathlib.Path(directory) / "Dux Files.app"
            executables = app / "Contents/MacOS"
            executables.mkdir(parents=True)
            with (app / "Contents/Info.plist").open("wb") as stream:
                plistlib.dump({"CFBundleExecutable": "Dux Files"}, stream)
            for name in ("Dux Files", "dfiles", "dfiles-native"):
                (executables / name).write_bytes(bytes.fromhex("cffaedfe") + bytes(32))
            hidden = app / "Contents/Resources/plugins/helper.dylib"
            hidden.parent.mkdir(parents=True)
            hidden.write_bytes(bytes.fromhex("cffaedfe") + bytes(32))
            system = "/usr/lib/libSystem.B.dylib"

            def otool(command, **_kwargs):
                path = pathlib.Path(command[-1])
                if "-hv" in command:
                    filetype = "DYLIB" if path == hidden else "EXECUTE"
                    output = f"{path}:\nMach header\nMH_MAGIC_64 ARM64 ALL 0x00 {filetype} 20 1712 NOUNDEFS\n"
                    return dependencies.subprocess.CompletedProcess(command, 0, output, "")
                dependency = "@rpath/libQt6Core.dylib" if path == hidden else system
                output = f"{path}:\n\t{dependency} (compatibility version 1.0.0, current version 1.0.0)\n"
                return dependencies.subprocess.CompletedProcess(command, 0, output, "")

            with patch.object(dependencies.subprocess, "run", side_effect=otool):
                failures = dependencies.check_bundle(app)
            self.assertEqual(len(failures), 2)
            self.assertIn("bundled dynamic library", failures[0])
            self.assertIn("@rpath/libQt6Core.dylib", failures[1])
            hidden.rename(hidden.with_name("renamed-plugin"))

            def system_only(command, **_kwargs):
                path = pathlib.Path(command[-1])
                if "-hv" in command:
                    filetype = "BUNDLE" if path.name == "renamed-plugin" else "EXECUTE"
                    output = f"{path}:\nMach header\nMH_MAGIC_64 ARM64 ALL 0x00 {filetype} 20 1712 NOUNDEFS\n"
                    return dependencies.subprocess.CompletedProcess(command, 0, output, "")
                return dependencies.subprocess.CompletedProcess(command, 0, f"{path}:\n\t{system} (compatibility version 1.0.0, current version 1.0.0)\n", "")

            with patch.object(dependencies.subprocess, "run", side_effect=system_only):
                failures = dependencies.check_bundle(app)
            self.assertEqual(len(failures), 1)
            self.assertIn("BUNDLE", failures[0])
            hidden.with_name("renamed-plugin").unlink()
            with patch.object(dependencies.subprocess, "run", side_effect=system_only):
                self.assertEqual(dependencies.check_bundle(app), [])

    def test_empty_bundle_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            app = pathlib.Path(directory)
            (app / "Contents").mkdir()
            with (app / "Contents/Info.plist").open("wb") as stream:
                plistlib.dump({"CFBundleExecutable": "Dux Files"}, stream)
            self.assertTrue(dependencies.check_bundle(app))


if __name__ == "__main__":
    unittest.main()
