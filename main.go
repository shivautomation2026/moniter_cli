package main

import (
	"embed"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var icon []byte

var appInstance *App

func main() {
	appInstance = NewApp()
	runWails()
}

func runWails() {
	app := application.New(application.Options{
		Name: "My App",
		Services: []application.Service{
			application.NewService(appInstance),
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "me.keemail.parakram.moniter_cli",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				appInstance.ShowWindow()
			},
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		Windows: application.WindowsOptions{
			DisableQuitOnLastWindowClosed: true,
		},
		Linux: application.LinuxOptions{
			DisableQuitOnLastWindowClosed: true,
		},
	})

	app.SetIcon(icon)
	appInstance.setApplication(app)

	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:        "My App",
		Width:        1024,
		Height:       768,
		HideOnEscape: true,
	})
	appInstance.setMainWindow(window)

	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if appInstance.shouldAllowQuit() {
			return
		}
		event.Cancel()
		appInstance.HideWindow()
	})

	tray := app.SystemTray.New()
	tray.SetIcon(icon)
	tray.SetTooltip("My App")
	tray.SetMenu(buildTray(app, appInstance))
	tray.AttachWindow(window)

	err := app.Run()
	if err != nil {
		panic(err)
	}
}
