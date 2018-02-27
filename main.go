// Copyright 2018 The Nakama Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"database/sql"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/golang/protobuf/jsonpb"
	"github.com/heroiclabs/nakama/migrate"
	"github.com/heroiclabs/nakama/server"
	"github.com/heroiclabs/nakama/social"
	_ "github.com/lib/pq"
	"go.uber.org/zap"
)

var (
	version  string = "2.0.0"
	commitID string = "dev"

	// Shared utility components.
	jsonpbMarshaler = &jsonpb.Marshaler{
		EnumsAsInts:  true,
		EmitDefaults: false,
		Indent:       "",
		OrigName:     false,
	}
	jsonpbUnmarshaler = &jsonpb.Unmarshaler{
		AllowUnknownFields: false,
	}
)

func main() {
	//startedAt := int64(time.Nanosecond) * time.Now().UTC().UnixNano() / int64(time.Millisecond)
	semver := fmt.Sprintf("%s+%s", version, commitID)
	// Always set default timeout on HTTP client.
	http.DefaultClient.Timeout = 1500 * time.Millisecond
	// Initialize the global random obj with customs seed.
	rand.Seed(time.Now().UnixNano())

	cmdLogger := server.NewJSONLogger(os.Stdout, true)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version":
			fmt.Println(semver)
			return
		case "migrate":
			migrate.Parse(os.Args[2:], cmdLogger)
		}
	}

	config := server.ParseArgs(cmdLogger, os.Args)
	jsonLogger, multiLogger := server.SetupLogging(config)

	multiLogger.Info("Nakama starting")
	multiLogger.Info("Node", zap.String("name", config.GetName()), zap.String("version", semver), zap.String("runtime", runtime.Version()))
	multiLogger.Info("Data directory", zap.String("path", config.GetDataDir()))
	multiLogger.Info("Database connections", zap.Strings("dsns", config.GetDatabase().Addresses))

	db, dbVersion := dbConnect(multiLogger, config.GetDatabase().Addresses)
	multiLogger.Info("Database information", zap.String("version", dbVersion))

	// Check migration status and log if the schema has diverged.
	migrate.StartupCheck(multiLogger, db)

	socialClient := social.NewClient(5 * time.Second)

	// Start up server components.
	registry := server.NewSessionRegistry()
	tracker := server.StartLocalTracker(jsonLogger, registry, jsonpbMarshaler, config.GetName())
	router := server.NewLocalMessageRouter(registry, tracker, jsonpbMarshaler)
	runtimePool, err := server.NewRuntimePool(jsonLogger, multiLogger, db, config, socialClient, registry, tracker, router)
	if err != nil {
		multiLogger.Fatal("Failed initializing runtime modules", zap.Error(err))
	}
	pipeline := server.NewPipeline(config, db, registry, tracker, router, runtimePool)
	apiServer := server.StartApiServer(jsonLogger, db, jsonpbMarshaler, jsonpbUnmarshaler, config, socialClient, registry, tracker, router, pipeline, runtimePool)

	// Respect OS stop signals.
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	multiLogger.Info("Startup done")

	// Wait for a termination signal.
	<-c
	multiLogger.Info("Shutting down")

	// Gracefully stop server components.
	apiServer.Stop()
	tracker.Stop()
	registry.Stop()

	os.Exit(0)
}

func dbConnect(multiLogger *zap.Logger, dsns []string) (*sql.DB, string) {
	// TODO config database pooling
	rawurl := fmt.Sprintf("postgresql://%s", dsns[0])
	url, err := url.Parse(rawurl)
	if err != nil {
		multiLogger.Fatal("Bad database connection URL", zap.Error(err))
	}
	query := url.Query()
	if len(query.Get("sslmode")) == 0 {
		query.Set("sslmode", "disable")
		url.RawQuery = query.Encode()
	}

	if len(parsedUrl.Path) < 1 {
		parsedUrl.Path = "/nakama"
	}

	db, err := sql.Open("postgres", parsedUrl.String())
	if err != nil {
		multiLogger.Fatal("Error connecting to database", zap.Error(err))
	}
	err = db.Ping()
	if err != nil {
		multiLogger.Fatal("Error pinging database", zap.Error(err))
	}

	var dbVersion string
	if err := db.QueryRow("SELECT version()").Scan(&dbVersion); err != nil {
		multiLogger.Fatal("Error querying database version", zap.Error(err))
	}

	return db, dbVersion
}
