import importlib.util
import pathlib
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("stage_qml", pathlib.Path(__file__).parents[1] / "stage-qml.py")
if spec is None or spec.loader is None:
    raise RuntimeError("Cannot load QML staging script")
staging = importlib.util.module_from_spec(spec)
spec.loader.exec_module(staging)


class StagingTests(unittest.TestCase):
    def test_restage_removes_deleted_components_and_assets(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            source, target = root / "source", root / "staged" / "qs"
            controls = source / "DCommon/Widgets/FloatingWindowControls.qml"
            controls.parent.mkdir(parents=True)
            controls.write_text('Item { property bool decorate: ownDecorations && Compositor.desktop === "kde" }')
            (source / "Theme.qml").write_text("pragma Singleton\nimport QtQuick\nQtObject {}\n")
            removed = source / "Old.qml"
            removed.write_text("import QtQuick\nItem {}\n")
            asset = source / "icon.svg"
            asset.write_text("<svg/>")
            staging.stage(source, target)
            self.assertIn("singleton Theme 1.0 Theme.qml", (target / "qmldir").read_text())
            self.assertIn('Qt.platform.os === "osx"', (target / controls.relative_to(source)).read_text())
            removed.unlink()
            asset.unlink()
            staging.stage(source, target)
            self.assertFalse((target / "Old.qml").exists())
            self.assertFalse((target / "icon.svg").exists())
            self.assertNotIn("Old", (target / "qmldir").read_text())

    def test_overlapping_paths_are_rejected_without_removing_source(self):
        with tempfile.TemporaryDirectory() as directory:
            source = pathlib.Path(directory) / "source"
            source.mkdir()
            marker = source / "shell.qml"
            marker.write_text("import QtQuick\nItem {}\n")
            for destination in (source, source / "staged", source.parent):
                with self.assertRaises(ValueError):
                    staging.stage(source, destination)
                self.assertTrue(marker.exists())


if __name__ == "__main__":
    unittest.main()
