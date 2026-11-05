import assert from "node:assert/strict";
import test from "node:test";
import vm from "node:vm";
import { lift, source } from "./qml-script.mjs";

const openFlow = source("../Modules/FileOpenFlow.qml");

function context(os = "linux", packages = []) {
    const ctx = vm.createContext({
        FilesFormat: { extractable: mime => mime === "application/zip" },
        Qt: { platform: { os } },
        NativePlatform: { isPackage: path => packages.includes(path) }
    });
    for (const name of ["isFolder", "openPlan", "openCount"])
        lift(openFlow, name, ctx);
    return ctx;
}

const folder = path => ({ path, isDir: true, mime: "inode/directory" });
const file = (path, mime = "text/plain") => ({ path, isDir: false, mime });

test("the first folder opens in the pane and the rest in tabs", () => {
    const ctx = context();
    const plan = ctx.openPlan([file("/a.txt"), folder("/one"), folder("/two"), folder("/three")], false);
    assert.equal(plan.folder, "/one");
    assert.deepEqual([...plan.tabs], ["/two", "/three"]);
    assert.deepEqual([...plan.files], ["/a.txt"]);
    assert.equal(ctx.openCount(plan), 3);
});

test("archives are unpacked only when extract on open is on", () => {
    const ctx = context();
    const entries = [file("/a.zip", "application/zip"), file("/b.txt")];
    const extracting = ctx.openPlan(entries, true);
    assert.deepEqual([...extracting.archives], ["/a.zip"]);
    assert.deepEqual([...extracting.files], ["/b.txt"]);
    const opening = ctx.openPlan(entries, false);
    assert.deepEqual([...opening.archives], []);
    assert.deepEqual([...opening.files], ["/a.zip", "/b.txt"]);
});

test("files alone leave the pane where it is", () => {
    const ctx = context();
    const plan = ctx.openPlan([file("/a.txt"), file("/b.txt")], false);
    assert.equal(plan.folder, "");
    assert.deepEqual([...plan.tabs], []);
    assert.equal(ctx.openCount(plan), 2);
});

test("macOS packages open as files while ordinary directories remain navigable", () => {
    const ctx = context("osx", ["/Editor.app", "/Document.pages"]);
    const plan = ctx.openPlan([folder("/Editor.app"), folder("/normal"), folder("/Document.pages")], false);
    assert.equal(plan.folder, "/normal");
    assert.deepEqual([...plan.files], ["/Editor.app", "/Document.pages"]);
    assert.equal(context().isFolder(folder("/Editor.app")), true);
});

test("activating a macOS package opens it instead of navigating into it", () => {
    const ctx = context("osx", ["/Editor.app"]);
    const opened = [];
    const navigated = [];
    Object.assign(ctx, {
        activePane: { trashActive: false, navigate: path => navigated.push(path) },
        SandboxService: { available: () => false },
        SettingsData: { extractOnOpen: false },
        AppsService: { open: paths => opened.push(...paths) }
    });
    lift(openFlow, "activate", ctx);
    ctx.activate(folder("/Editor.app"));
    ctx.activate(folder("/normal"));
    assert.deepEqual(opened, ["/Editor.app"]);
    assert.deepEqual(navigated, ["/normal"]);
});
