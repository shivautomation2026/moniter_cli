import {
    HasConfig,
    SaveConfigAndStart,
    PickFolder,
    StopMonitor,
    GetStatus
} from "../wailsjs/go/main/App";

document.getElementById("pickPdfFolder").addEventListener("click", async () => {
    try {
        const path = await PickFolder();
        if (path) {
            document.getElementById("pdf_folder").value = path;
        }
    } catch (err) {
        console.error(err);
    }
});
const form = document.getElementById("configForm");
const output = document.getElementById("output");
const setupSection = document.getElementById("setupSection");
const runningSection = document.getElementById("runningSection");
const runningStatus = document.getElementById("runningStatus");

const pdfFolderInput = document.getElementById("pdf_folder");
const baseOutputFolderInput = document.getElementById("base_output_folder");

const pickPdfFolderBtn = document.getElementById("pickPdfFolder");
const pickBaseOutputFolderBtn = document.getElementById("pickBaseOutputFolder");
const stopMonitorBtn = document.getElementById("stopMonitorBtn");

function showMessage(msg) {
    output.textContent = String(msg ?? "");
}

function showSetup() {
    setupSection.style.display = "block";
    runningSection.style.display = "none";
}

function showRunning(message) {
    setupSection.style.display = "none";
    runningSection.style.display = "block";
    runningStatus.textContent = message || "Monitor is running.";
}

// pickPdfFolderBtn.addEventListener("click", async () => {
//     try {
//         const path = await PickFolder();
//         if (path) pdfFolderInput.value = path;
//     } catch (err) {
//         showMessage("Folder picker error: " + err);
//     }
// });

pickBaseOutputFolderBtn.addEventListener("click", async () => {
    try {
        const path = await PickFolder();
        if (path) baseOutputFolderInput.value = path;
    } catch (err) {
        showMessage("Folder picker error: " + err);
    }
});

form.addEventListener("submit", async (e) => {
    e.preventDefault();

    if (!pdfFolderInput.value || !baseOutputFolderInput.value) {
        showMessage("Please select both folders.");
        return;
    }

    const data = new FormData(form);
    const config = Object.fromEntries(data.entries());

    try {
        showMessage("Saving config and starting monitor...");
        const result = await SaveConfigAndStart(config);
        showRunning(result);
        showMessage(result);
    } catch (err) {
        showMessage("Submit error: " + err);
    }
});

stopMonitorBtn.addEventListener("click", async () => {
    try {
        await StopMonitor();
        showSetup();
        showMessage("Monitor stopped.");
    } catch (err) {
        showMessage("Stop error: " + err);
    }
});
const hideBtn = document.getElementById("hideWindowBtn");

if (hideBtn) {
    hideBtn.addEventListener("click", async () => {
        try {
            await HideWindow();
        } catch (err) {
            showMessage("Hide error: " + err);
        }
    });
}

async function init() {
    try {
        const hasConfig = await HasConfig();
        const status = await GetStatus();

        if (hasConfig && status.toLowerCase().includes("running")) {
            showRunning(status);
        } else {
            showSetup();
        }

        showMessage(status);
    } catch (err) {
        showSetup();
        showMessage("Startup error: " + err);
    }
}

init();