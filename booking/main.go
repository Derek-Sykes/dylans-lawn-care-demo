package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) > 1 {
		if os.Args[1] == "operator-config" {
			os.Exit(runOperatorConfigCommand(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, loadConfig))
		}
		os.Exit(runGoogleConfigCommand(os.Args[1:], os.Stdin, os.Stderr, loadConfig))
	}
	config, err := loadConfig()
	if err != nil {
		log.Fatal("Booking configuration is invalid: ", err)
	}
	app, err := newApp(config)
	if err != nil {
		log.Fatal("Booking storage could not be opened. Check the persistent volume and its encryption key.")
	}
	defer app.store.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Print("Booking service ready on public port 8081 and owner port 8082.")
	if err = app.run(ctx); err != nil {
		log.Fatal("Booking listener stopped unexpectedly.")
	}
}
