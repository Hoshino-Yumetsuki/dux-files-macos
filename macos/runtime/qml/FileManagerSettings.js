    function setFileManager(enabled) {
        DuxFilesService.sendRequest("system.fileManager.set", {
            "enabled": enabled
        }, response => {
            if (response.error) {
                ToastService.info(response.error);
                root.loadFileManagerStatus();
                return;
            }
            root.fileManagerStatus = response.result || {};
            SettingsData.registerFileManager = root.fileManagerStatus.enabled === true;
        });
    }

    function fileManagerDescription() {
        const status = fileManagerStatus;
        if ((status.reason ?? "") !== "")
            return I18n.tr("Default folder handler: %1", "macOS folder-opening setting error, %1 is the native error").arg(status.reason);
        return I18n.tr("Changes default folder opening only. macOS open and save dialogs are unchanged.", "macOS default folder-opening setting description");
    }
