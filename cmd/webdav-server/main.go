package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/mniehe/davkit"
)

func main() {
	var addr string
	flag.StringVar(&addr, "addr", ":8080", "listening address")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: %s [options...] [directory]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	path := flag.Arg(0)
	if path == "" {
		path = "."
	}

	handler := webdav.Handler{
		FileSystem: webdav.LocalFileSystem(path),
	}
	log.Printf("WebDAV server listening on %v", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           &handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
