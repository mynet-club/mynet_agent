// Native browser scripting: URL arguments are data, never interpolated source.
function openPanelInBrowser(browser, target, key, safari) {
    browser.activate();
    var windows = browser.windows();
    for (var wi = 0; wi < windows.length; wi++) {
        var tabs = windows[wi].tabs();
        for (var ti = 0; ti < tabs.length; ti++) {
            var current = tabs[ti].url();
            if (typeof current !== "string" || current.split(/[?#]/)[0] !== key) continue;
            if (current !== target) tabs[ti].url = target;
            if (safari) windows[wi].currentTab = tabs[ti];
            else windows[wi].activeTabIndex = ti + 1;
            windows[wi].index = 1;
            browser.activate();
            return "reused";
        }
    }
    if (windows.length === 0) {
        if (safari) browser.Document().make();
        else browser.Window().make();
        var created = browser.windows()[0];
        if (safari) created.currentTab.url = target;
        else created.activeTab.url = target;
    } else {
        var win = windows[0];
        win.tabs.push(browser.Tab({url: target}));
        if (safari) win.currentTab = win.tabs[win.tabs().length - 1];
        else win.activeTabIndex = win.tabs().length;
        win.index = 1;
    }
    browser.activate();
    return "created";
}

function run(argv) {
    ObjC.import("AppKit");
    var appURL = $.NSWorkspace.sharedWorkspace.URLForApplicationToOpenURL($.NSURL.URLWithString(argv[0]));
    if (!appURL) return "unsupported";
    var bundle = ObjC.unwrap($.NSBundle.bundleWithURL(appURL).bundleIdentifier);
    var safari = bundle === "com.apple.Safari" || bundle === "com.apple.SafariTechnologyPreview";
    var chromium = ["com.google.Chrome", "com.google.Chrome.canary", "com.microsoft.edgemac",
        "com.microsoft.edgemac.Beta", "com.microsoft.edgemac.Dev", "com.brave.Browser", "com.vivaldi.Vivaldi"];
    if (!safari && chromium.indexOf(bundle) < 0) return "unsupported";
    return openPanelInBrowser(Application(bundle), argv[0], argv[1], safari);
}
