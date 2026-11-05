"""Run against a built macOS host: python3 -I macos/tests/test_host.py APP_EXECUTABLE."""

import importlib.util
import json
import os
import pathlib
import plistlib
import queue
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest


BINARY = pathlib.Path(sys.argv.pop(1)).resolve() if len(sys.argv) > 1 and pathlib.Path(sys.argv[1]).is_file() else None


@unittest.skipUnless(BINARY and BINARY.is_file(), "Pass the built Dux Files executable")
class HostTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="dh-", dir="/tmp")
        self.addCleanup(self.directory.cleanup)
        self.root = pathlib.Path(self.directory.name)
        self.env = {key: value for key, value in os.environ.items()
                    if not key.startswith(("DFILES_", "QT_", "QML"))}
        self.env["QT_QPA_PLATFORM"] = "offscreen"
        for key, name in (("HOME", "home"), ("XDG_CONFIG_HOME", "config"),
                          ("XDG_DATA_HOME", "data"), ("XDG_STATE_HOME", "state dir"),
                          ("XDG_CACHE_HOME", "cache dir"), ("XDG_RUNTIME_DIR", "run")):
            path = self.root / name
            path.mkdir()
            self.env[key] = str(path)

    def test_qml_failure_is_rejected_even_after_pass_marker(self):
        runner_path = pathlib.Path(__file__).resolve().parents[2] / "quickshell/tests/run-qml.py"
        spec = importlib.util.spec_from_file_location("qml_runner", runner_path)
        assert spec is not None and spec.loader is not None
        runner = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(runner)
        fixture = self.root / "failure.qml"
        fixture.write_text('''import QtQuick
import QtTest
Item {
    TestCase {
        name: "FailureProbe"
        when: true
        function test_failure() { compare(1, 2); }
        function cleanupTestCase() { console.log("FIXTURE PASS"); }
    }
}
''')
        _, passed, _, output = runner.run_fixture(fixture, str(BINARY), True)
        self.assertIn("FIXTURE PASS", output)
        self.assertIn("QML test failures: 1", output)
        self.assertFalse(passed, output)

    def test_folder_handler_matches_native_default_opener(self):
        assert BINARY is not None
        helper = BINARY.with_name("dfiles-native")
        self.assertTrue(helper.is_file(), "The bundled native helper must exist")

        def invoke(method, params):
            result = subprocess.run([str(helper)], input=json.dumps({"method": method, "params": params}),
                                    env=self.env, capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            return json.loads(result.stdout)

        owner = invoke("apps.folderHandler.get", {})["owner"]
        catalog = invoke("apps.query", {"mimes": ["inode/directory"]})
        application = pathlib.Path(catalog["defaults"]["inode/directory"])
        with (application / "Contents/Info.plist").open("rb") as stream:
            identifier = plistlib.load(stream)["CFBundleIdentifier"]
        self.assertEqual(owner.casefold(), identifier.casefold())

    def test_native_paths_and_session_follow_isolated_environment(self):
        assert BINARY is not None
        source = BINARY.parents[1] / "Resources/qml/qs"
        shell = self.root / "qml" / "qs"
        shell.mkdir(parents=True)
        for entry in source.iterdir():
            if entry.name != "shell.qml":
                (shell / entry.name).symlink_to(entry)
        (shell / "shell.qml").write_text('''import QtQuick
import qs.Common
Window {
    visible: true
    width: 200
    height: 120
    Component.onCompleted: {
        console.log("PATH-PROBE " + JSON.stringify([Paths.strip(Paths.home), Paths.strip(Paths.xdgCache), Paths.strip(Paths.stateDir)]));
        SessionData.windowWidth = 743;
    }
}
''')
        result = subprocess.run([str(BINARY), "--no-backend", "--smoke-test", "--config", str(shell)],
                                env=self.env, capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        marker = next(line.split("PATH-PROBE ", 1)[1] for line in result.stderr.splitlines() if "PATH-PROBE " in line)
        self.assertEqual(json.loads(marker), [self.env["HOME"], self.env["XDG_CACHE_HOME"],
                                              self.env["XDG_STATE_HOME"] + "/dfiles"])
        state = pathlib.Path(self.env["XDG_STATE_HOME"]) / "dfiles/session.json"
        self.assertEqual(json.loads(state.read_text())["windowWidth"], 743)

    def test_full_disk_access_offer_is_optional_and_shown_once(self):
        assert BINARY is not None
        source = BINARY.parents[1] / "Resources/qml/qs"
        shell = self.root / "qml" / "qs"
        shell.mkdir(parents=True)
        for entry in source.iterdir():
            if entry.name != "shell.qml":
                (shell / entry.name).symlink_to(entry)
        (shell / "shell.qml").write_text('''import QtQuick
import QtTest
import Quickshell
import qs.Common
import qs.Services
Item {
    id: root
    property var application: null
    Component.onCompleted: application = Qt.createComponent(%s).createObject(root)
    TestCase {
        name: "FullDiskAccessOffer"
        when: root.application !== null
        function test_offer() {
            tryVerify(() => WindowService.count > 0);
            const window = root.application.windowObject(WindowService.primaryId);
            const dialog = findChild(window.nativeFileTools, "fullDiskAccessDialog");
            verify(dialog !== null);
            const firstLaunch = Quickshell.env("DFILES_TEST_FIRST_LAUNCH") === "1";
            if (firstLaunch) {
                tryCompare(dialog, "visible", true);
                verify(window.dialogOpen);
                mouseClick(dialog.actions[0]);
                tryCompare(window, "dialogOpen", false);
            } else {
                wait(100);
                compare(dialog.visible, false);
                compare(window.dialogOpen, false);
            }
            const secondId = WindowService.create([]);
            const second = root.application.windowObject(secondId);
            wait(100);
            compare(second.dialogOpen, false);
            WindowService.close(window.windowId);
            wait(100);
            compare(second.dialogOpen, false);
            SettingsData.flush();
        }
        function cleanupTestCase() { Qt.exit(qtest_results.failCount ? 1 : 0); }
    }
    Timer { interval: 8000; running: true; onTriggered: Qt.exit(2) }
}
''' % json.dumps((source / "shell.qml").as_uri()))
        for first_launch in ("1", "0"):
            with self.subTest(first_launch=first_launch):
                result = subprocess.run([str(BINARY), "--no-backend", "--config", str(shell)],
                                        env=dict(self.env, DFILES_TEST_FIRST_LAUNCH=first_launch),
                                        capture_output=True, text=True, timeout=12)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_finder_refresh_retains_previous_list_until_success(self):
        assert BINARY is not None
        executable = self.root / "Dux Files"
        os.link(BINARY, executable)
        helper = self.root / "dfiles-native"
        helper.write_text('''#!/bin/sh
/bin/cat >/dev/null
step=0
if [ -f "$DFILES_FINDER_STEP" ]; then read -r step < "$DFILES_FINDER_STEP"; fi
step=$((step + 1))
printf '%s\\n' "$step" > "$DFILES_FINDER_STEP"
case "$step" in
    1|3) printf '%s\\n' '{"error":"Shared list unavailable"}'; exit 1 ;;
    2) printf '%s\\n' '{"entries":[{"section":"favorites","path":"/first"}]}' ;;
    4) printf '%s\\n' 'invalid JSON' ;;
    5) printf '%s\\n' '{"entries":[{"section":"locations","path":"/replacement"}]}' ;;
    6) printf '%s\\n' '{"entries":[]}' ;;
esac
''')
        helper.chmod(0o700)
        shell = self.root / "qml"
        shell.mkdir()
        (shell / "shell.qml").write_text('''import QtQuick
import Quickshell
Item {
    id: root
    property int replies: 0
    Component.onCompleted: NativePlatform.refreshFinderPlaces()
    Connections {
        target: NativePlatform
        function onFinderPlacesChanged() {
            console.log("FINDER-STATE " + JSON.stringify({paths: NativePlatform.finderPlaces.map(place => place.path), error: NativePlatform.finderPlacesError !== ""}));
            root.replies++;
            if (root.replies === 6) { Qt.quit(); return; }
            Qt.callLater(() => NativePlatform.refreshFinderPlaces());
        }
    }
    Timer { interval: 5000; running: true; onTriggered: Qt.exit(1) }
}
''')
        result = subprocess.run([str(executable), "--no-backend", "--config", str(shell)],
                                env=dict(self.env, DFILES_FINDER_STEP=str(self.root / "step")),
                                capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        states = [json.loads(line.split("FINDER-STATE ", 1)[1])
                  for line in result.stderr.splitlines() if "FINDER-STATE " in line]
        self.assertEqual(states, [
            {"paths": [], "error": True},
            {"paths": ["/first"], "error": False},
            {"paths": ["/first"], "error": True},
            {"paths": ["/first"], "error": True},
            {"paths": ["/replacement"], "error": False},
            {"paths": [], "error": False},
        ])

    def test_floating_window_fits_screen_and_preserves_reopened_position(self):
        assert BINARY is not None
        shell = self.root / "placement"
        shell.mkdir()
        (shell / "shell.qml").write_text('''import QtQuick
import Quickshell
FloatingWindow {
    id: window
    visible: true
    implicitWidth: 5000
    implicitHeight: 4000
    minimumSize: Qt.size(120, 80)
    color: "navy"
    Timer {
        interval: 200; running: true
        onTriggered: {
            const area = window.screen.availableGeometry;
            console.log("PLACEMENT " + JSON.stringify({x: window.x, y: window.y, width: window.width, height: window.height, area: {x: area.x, y: area.y, width: area.width, height: area.height}}));
            window.visible = false;
            window.width = 240; window.height = 160;
            window.x = area.x + 30; window.y = area.y + 40;
            window.visible = true;
            console.log("REOPEN " + JSON.stringify([window.x, window.y, area.x + 30, area.y + 40]));
        }
    }
}
''')
        result = subprocess.run([str(BINARY), "--no-backend", "--smoke-test", "--config", str(shell)],
                                env=self.env, capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        geometry = json.loads(next(line.split("PLACEMENT ", 1)[1] for line in result.stderr.splitlines() if "PLACEMENT " in line))
        area = geometry["area"]
        self.assertGreaterEqual(geometry["x"], area["x"])
        self.assertGreaterEqual(geometry["y"], area["y"])
        self.assertLessEqual(geometry["x"] + geometry["width"], area["x"] + area["width"])
        self.assertLessEqual(geometry["y"] + geometry["height"], area["y"] + area["height"])
        reopened = json.loads(next(line.split("REOPEN ", 1)[1] for line in result.stderr.splitlines() if "REOPEN " in line))
        self.assertEqual(reopened[:2], reopened[2:])

    def test_main_window_restores_size_before_first_show(self):
        assert BINARY is not None
        state = self.root / "state dir/dfiles"
        state.mkdir()
        (state / "session.json").write_text(json.dumps({"windowWidth": 600, "windowHeight": 400}))
        shell = self.root / "qml/qs"
        shell.mkdir(parents=True)
        source = BINARY.parents[1] / "Resources/qml/qs"
        for entry in source.iterdir():
            if entry.name == "shell.qml":
                (shell / "OriginalShell.qml").write_text(entry.read_text())
                continue
            (shell / entry.name).symlink_to(entry)
        (shell / "shell.qml").write_text('''import QtQuick
import qs.Services
Item {
    OriginalShell { id: original }
    Timer {
        interval: 200; running: true; repeat: true
        onTriggered: {
            const window = original.windowObject(WindowService.target());
            if (window && window.visible) {
                console.log("RESTORED-SIZE " + JSON.stringify([window.width, window.height]));
                stop();
            }
        }
    }
}
''')
        result = subprocess.run([str(BINARY), "--smoke-test", "--config", str(shell)],
                                env=self.env, capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        size = json.loads(next(line.split("RESTORED-SIZE ", 1)[1] for line in result.stderr.splitlines() if "RESTORED-SIZE " in line))
        self.assertEqual(size, [600, 400])

    def test_runtime_directory_fallbacks_render(self):
        for mode in ("missing", "empty", "unset"):
            with self.subTest(mode=mode):
                env = dict(self.env)
                if mode == "missing":
                    env["XDG_RUNTIME_DIR"] = str(self.root / "new" / "run")
                elif mode == "empty":
                    env["XDG_RUNTIME_DIR"] = ""
                else:
                    env.pop("XDG_RUNTIME_DIR", None)
                    env.pop("TMPDIR", None)
                result = subprocess.run([str(BINARY), "--smoke-test"], env=env,
                                        capture_output=True, text=True, timeout=25)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("Dux Files rendered", result.stderr)

    def test_relative_paths_and_alternate_imports_with_fragmented_reply(self):
        shell = self.root / "qml" / "qs"
        common = shell / "Common"
        common.mkdir(parents=True)
        (common / "qmldir").write_text("module qs.Common\nsingleton HostProbe 1.0 HostProbe.qml\n")
        (common / "HostProbe.qml").write_text('pragma Singleton\nimport QtQuick\nQtObject { readonly property string marker: "alternate-import-root" }\n')
        (shell / "shell.qml").write_text('''import QtQuick
import qs.Common
Window {
    visible: true
    width: 200
    height: 120
    Component.onCompleted: console.log(HostProbe.marker)
}
''')
        target = self.root / "folder"
        target.mkdir()
        socket_path = self.root / "backend.sock"
        responses = queue.Queue()
        with socket.socket(socket.AF_UNIX) as server:
            server.bind(str(socket_path))
            server.listen()
            server.settimeout(8)

            def serve():
                try:
                    with server.accept()[0] as client:
                        client.settimeout(5)
                        client.sendall(b'{"apiVersion":1}\n')
                        request = json.loads(client.makefile("rb").readline())
                        client.sendall(b'{"id":')
                        time.sleep(.04)
                        client.sendall(b'1,"result":{"ok":true}}\n')
                        responses.put(request)
                except Exception as error:
                    responses.put(error)

            worker = threading.Thread(target=serve, daemon=True)
            worker.start()
            env = dict(self.env, DFILES_SOCKET=str(socket_path))
            result = subprocess.run([str(BINARY), "--no-backend", "--smoke-test",
                                     "--config", str(shell), "folder"], cwd=self.root,
                                    env=env, capture_output=True, text=True, timeout=15)
            worker.join(timeout=8)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("alternate-import-root", result.stderr)
        request = responses.get(timeout=1)
        if isinstance(request, Exception):
            raise request
        self.assertEqual(request["method"], "ui.open")
        self.assertEqual(request["params"]["paths"], [str(target.resolve())])

    def test_open_retries_when_backend_is_not_listening(self):
        shell = self.root / "qs"
        shell.mkdir()
        (shell / "shell.qml").write_text('''import QtQuick
Window {
    visible: true
    width: 200
    height: 120
    Component.onCompleted: console.log("waiting-for-backend")
}
''')
        socket_path = self.root / "backend.sock"
        env = dict(self.env, DFILES_SOCKET=str(socket_path))
        app = subprocess.Popen([str(BINARY), "--no-backend", "--smoke-test",
                                "--config", str(shell), str(self.root)],
                               env=env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
                               text=True)
        try:
            output = []
            assert app.stderr is not None
            for line in app.stderr:
                output.append(line)
                if "waiting-for-backend" in line:
                    break
            else:
                self.fail("Host did not load the test window: " + "".join(output))
            time.sleep(.15)
            with socket.socket(socket.AF_UNIX) as server:
                server.bind(str(socket_path))
                server.listen()
                server.settimeout(3)
                with server.accept()[0] as client:
                    client.settimeout(2)
                    request = json.loads(client.makefile("rb").readline())
                    self.assertEqual(request["params"]["paths"], [str(self.root)])
                    client.sendall(b'{"id":1,"result":{"ok":true}}\n')
            _, stderr = app.communicate(timeout=10)
            self.assertEqual(app.returncode, 0, "".join(output) + stderr)
        finally:
            if app.poll() is None:
                app.kill()
                app.communicate()

    def test_open_timeout_does_not_replay_delivered_request(self):
        shell = self.root / "qs"
        shell.mkdir()
        (shell / "shell.qml").write_text('''import QtQuick
Window {
    visible: true
    width: 200
    height: 120
    Timer { interval: 6500; running: true; onTriggered: Qt.quit() }
}
''')
        socket_path = self.root / "backend.sock"
        env = dict(self.env, DFILES_SOCKET=str(socket_path))
        with socket.socket(socket.AF_UNIX) as server:
            server.bind(str(socket_path))
            server.listen()
            server.settimeout(8)
            app = subprocess.Popen([str(BINARY), "--no-backend", "--config", str(shell), str(self.root)],
                                   env=env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
            try:
                with server.accept()[0] as client:
                    client.settimeout(7)
                    reader = client.makefile("rb")
                    self.assertEqual(json.loads(reader.readline())["method"], "ui.open")
                    self.assertEqual(reader.read(), b"", "Host must time out an unacknowledged request")
                server.settimeout(.4)
                with self.assertRaises(socket.timeout):
                    server.accept()
                _, stderr = app.communicate(timeout=5)
                self.assertEqual(app.returncode, 0, stderr)
                self.assertIn("Timed out delivering open paths", stderr)
            finally:
                if app.poll() is None:
                    app.kill()
                    app.communicate()

    def test_native_copy_and_ui_quit_remove_daemon_socket(self):
        assert BINARY is not None
        runtime = self.root / "run"
        env = dict(self.env, XDG_RUNTIME_DIR=str(runtime))
        with (self.root / "host.log").open("w+") as log:
            app = subprocess.Popen([str(BINARY)], env=env, stdout=log, stderr=subprocess.STDOUT)
            try:
                deadline = time.monotonic() + 15
                while time.monotonic() < deadline:
                    paths = list(runtime.glob("dfiles-*.sock"))
                    if paths:
                        break
                    self.assertIsNone(app.poll(), "Host exited before the daemon was ready")
                    time.sleep(.05)
                else:
                    self.fail("Daemon socket did not appear")
                time.sleep(1)
                shown = subprocess.run([str(BINARY.with_name("dfiles")), "run"], env=env,
                                       capture_output=True, text=True, timeout=12)
                self.assertEqual(shown.returncode, 0, shown.stderr)
                self.assertIn("already running; showing window", shown.stderr)
                self.assertEqual(list(runtime.glob("dfiles-*.sock")), paths)
                with socket.socket(socket.AF_UNIX) as client:
                    client.settimeout(4)
                    client.connect(str(paths[0]))
                    reader = client.makefile("rb")

                    def call(method, params=None):
                        client.sendall(json.dumps({"id": 7, "method": method, "params": params or {}}).encode() + b"\n")
                        while True:
                            line = reader.readline()
                            self.assertTrue(line, "Daemon closed before acknowledging request")
                            response = json.loads(line)
                            if response.get("id") == 7:
                                self.assertNotIn("error", response)
                                return response.get("result")

                    source = self.root / "home/source.txt"
                    source.write_bytes(b"native IPC round trip")
                    destination = self.root / "copied"
                    destination.mkdir()
                    job = call("files.op.copy", {"paths": [str(source)], "dest": str(destination)})["job"]
                    deadline = time.monotonic() + 10
                    while time.monotonic() < deadline:
                        job = next(item for item in call("files.op.jobs")["jobs"] if item["id"] == job["id"])
                        self.assertNotIn(job["state"], ("failed", "paused-error", "paused-conflict"), job)
                        if job["state"] == "done":
                            break
                        time.sleep(.05)
                    self.assertEqual(job["state"], "done", job)
                    self.assertFalse(job["skipped"] or job["cancelled"], job)
                    self.assertEqual((destination / source.name).read_bytes(), source.read_bytes())
                    call("ui.quit")
                self.assertEqual(app.wait(timeout=10), 0)
                self.assertFalse(paths[0].exists(), "Daemon socket leaked after quit")
            finally:
                if app.poll() is None:
                    app.terminate()
                    try:
                        app.wait(timeout=7)
                    except subprocess.TimeoutExpired:
                        app.kill()
                        app.wait()


    def test_ui_crash_removes_backend_socket_and_tracking(self):
        assert BINARY is not None
        for mode in ("host", "cli"):
            with self.subTest(mode=mode):
                runtime = self.root / mode
                runtime.mkdir()
                env = dict(self.env, XDG_RUNTIME_DIR=str(runtime))
                args = [str(BINARY)] if mode == "host" else [str(BINARY.with_name("dfiles")), "run"]
                with (self.root / (mode + ".log")).open("w+") as log:
                    app = subprocess.Popen(args, env=env, stdout=log, stderr=subprocess.STDOUT)
                    backend_pid = None
                    try:
                        deadline = time.monotonic() + 15
                        while time.monotonic() < deadline:
                            states = list(runtime.glob("dfiles-*.pid"))
                            if states and states[0].with_suffix(".sock").exists():
                                break
                            self.assertIsNone(app.poll(), "Host exited before backend startup")
                            time.sleep(.05)
                        else:
                            self.fail("Backend did not become ready")
                        state = states[0]
                        backend_pid = int(state.stem.removeprefix("dfiles-"))
                        ui_pid = int(state.read_text())
                        os.kill(ui_pid, signal.SIGKILL)
                        self.assertEqual(app.wait(timeout=10), -signal.SIGKILL if mode == "host" else 137)
                        deadline = time.monotonic() + 8
                        while time.monotonic() < deadline and list(runtime.glob("dfiles*")):
                            time.sleep(.05)
                        self.assertEqual(list(runtime.glob("dfiles*")), [], "Crash left runtime files behind")
                        if mode == "host":
                            while time.monotonic() < deadline:
                                try:
                                    os.kill(backend_pid, 0)
                                except ProcessLookupError:
                                    break
                                time.sleep(.05)
                            else:
                                self.fail("Daemon remained alive after native UI crash")
                    finally:
                        if app.poll() is None:
                            app.terminate()
                            try:
                                app.wait(timeout=7)
                            except subprocess.TimeoutExpired:
                                app.kill()
                                app.wait()
                        if mode == "host" and backend_pid is not None:
                            try:
                                os.kill(backend_pid, signal.SIGTERM)
                            except ProcessLookupError:
                                pass

if __name__ == "__main__":
    unittest.main()
