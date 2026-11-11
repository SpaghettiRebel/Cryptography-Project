// Клиент Крипточата: окно Wails, интерфейс на React, логика в internal/service.
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"cryptochat/client/internal/service"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	if _, err := fs.Stat(assets, "frontend/dist/index.html"); err != nil && !generatingBindings {
		fmt.Fprintln(os.Stderr, "интерфейс не собран: выполните client/scripts/build.sh")
		os.Exit(1)
	}
	svc := service.New(service.Config{})
	app := NewApp(svc)
	app.media = &mediaCounter{h: svc.Media()}
	err := wails.Run(&options.App{
		Title:  "Крипточат",
		Width:  1100,
		Height: 720,
		// Запросы, для которых нет файла интерфейса, получает обработчик картинок /media/<seq>.
		AssetServer: &assetserver.Options{Assets: assets, Handler: app.media},
		OnStartup:   app.startup,
		OnShutdown:  app.shutdown,
		Bind:        []interface{}{app},
	})
	if err != nil {
		log.Println("окно:", err)
		os.Exit(1)
	}
}
