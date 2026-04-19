// Copyright 2026 Brian Bouterse
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command bridge is the Alcove controller — the central coordinator that
// provides the REST API, dispatches tasks to Skiff pods, and serves the
// dashboard.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bmbouter/alcove/internal/auth"
	"github.com/bmbouter/alcove/internal/bridge"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("alcove-bridge %s starting", Version)

	// Load configuration.
	cfg, err := bridge.LoadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}
	cfg.Version = Version
	log.Printf("runtime=%s port=%s", cfg.RuntimeType, cfg.Port)

	// Connect to NATS (Hail).
	nc, err := nats.Connect(cfg.HailURL,
		nats.Name("alcove-bridge"),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Printf("hail disconnected: %v", err)
			}
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			log.Println("hail reconnected")
		}),
	)
	if err != nil {
		log.Fatalf("connecting to hail (NATS) at %s: %v", cfg.HailURL, err)
	}
	defer nc.Close()
	log.Printf("connected to hail at %s", cfg.HailURL)

	// Connect to PostgreSQL (Ledger).
	dbpool, err := pgxpool.New(context.Background(), cfg.LedgerURL)
	if err != nil {
		log.Fatalf("connecting to ledger (PostgreSQL): %v", err)
	}
	defer dbpool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := dbpool.Ping(ctx); err != nil {
		log.Fatalf("ledger ping failed: %v", err)
	}
	cancel()
	log.Println("connected to ledger (PostgreSQL)")

	// Run database migrations.
	if err := bridge.Migrate(context.Background(), dbpool); err != nil {
		log.Fatalf("database migration failed: %v", err)
	}

	// Initialize the container runtime.
	rt, err := bridge.NewRuntime(cfg.RuntimeType, os.Getenv("SHIM_BIN_PATH"))
	if err != nil {
		log.Fatalf("initializing runtime: %v", err)
	}
	log.Printf("runtime initialized: %s", cfg.RuntimeType)

	// Build the user store for authentication.
	var store auth.Authenticator
	var mgr auth.UserManager

	switch cfg.AuthBackend {
	case "memory":
		hash, err := auth.HashPassword("admin")
		if err != nil {
			log.Fatalf("hashing default admin password: %v", err)
		}
		userMap := map[string]string{"admin": hash}
		log.Printf("default admin user created — username: admin, password: admin — change this in the dashboard")
		store = auth.NewMemoryStore(userMap)
		log.Printf("auth backend: memory (%d user(s) loaded)", len(userMap))

	case "postgres":
		pgStore := auth.NewPgStore(dbpool)
		// If no users exist, create default admin.
		users, err := pgStore.ListUsers(context.Background())
		if err != nil {
			log.Fatalf("listing users: %v", err)
		}
		if len(users) == 0 {
			if err := pgStore.CreateUser(context.Background(), "admin", "admin", true); err != nil {
				log.Fatalf("creating default admin: %v", err)
			}
			log.Printf("default admin user created — username: admin, password: admin — change this in the dashboard")
		}
		// ADMIN_RESET_PASSWORD: force-create or reset the admin user.
		// Used when switching from rh-identity to postgres backend on a
		// database that already has SSO-provisioned users (who have no passwords).
		if resetPw := os.Getenv("ADMIN_RESET_PASSWORD"); resetPw != "" {
			// Try to delete existing admin, ignore errors (may not exist).
			_ = pgStore.DeleteUser(context.Background(), "admin")
			if err := pgStore.CreateUser(context.Background(), "admin", resetPw, true); err != nil {
				log.Fatalf("resetting admin password: %v", err)
			}
			log.Printf("admin user reset with provided password (ADMIN_RESET_PASSWORD)")
		}
		store = pgStore
		if m, ok := store.(auth.UserManager); ok {
			mgr = m
		}
		log.Printf("auth backend: postgres (%d user(s) in database)", len(users))

	case "rh-identity":
		rhStore := auth.NewRHIdentityStore(dbpool)
		if err := rhStore.BootstrapAdmins(context.Background(), cfg.RHIdentityAdmins); err != nil {
			log.Fatalf("bootstrapping rh-identity admins: %v", err)
		}
		store = rhStore
		if m, ok := store.(auth.UserManager); ok {
			mgr = m
		}
		log.Printf("auth backend: rh-identity (trusted header)")
	}

	// Create credential store and migrate env-based credentials.
	credStore := bridge.NewCredentialStore(dbpool, cfg.DatabaseEncryptionKey)
	credStore.MigrateFromEnv(context.Background(), cfg)

	// Create tool store and seed builtin tools.
	toolStore := bridge.NewToolStore(dbpool)
	if err := toolStore.SeedBuiltinTools(context.Background()); err != nil {
		log.Fatalf("seeding builtin tools: %v", err)
	}
	log.Println("builtin MCP tools seeded")

	// Create security profile store.
	profileStore := bridge.NewProfileStore(dbpool)

	// Create settings store for admin settings.
	settingsStore := bridge.NewSettingsStore(dbpool)

	// Create system LLM client for AI-powered Bridge features.
	bridgeLLM := bridge.NewBridgeLLM(cfg, credStore)
	if bridgeLLM != nil {
		log.Println("system LLM configured")
	} else {
		log.Println("system LLM not configured — add system_llm section to alcove.yaml to enable AI features")
	}

	// Create dispatcher and API.
	dispatcher := bridge.NewDispatcher(nc, dbpool, rt, cfg, credStore, toolStore, profileStore, settingsStore)

	// Create CI Gate monitor for automated CI retry.
	ciGateMonitor := bridge.NewCIGateMonitor(dbpool, dispatcher, credStore, bridgeLLM)
	dispatcher.SetCIGateMonitor(ciGateMonitor)
	log.Println("CI gate monitor initialized")

	// Start listening for status updates from Skiff pods.
	if err := dispatcher.ListenForStatusUpdates(context.Background()); err != nil {
		log.Fatalf("subscribing to status updates: %v", err)
	}

	// Recover state from previous Bridge instance.
	dispatcher.RecoverHandles(context.Background())
	go dispatcher.ReconcileLoop(context.Background())
	ciGateMonitor.RecoverMonitors(context.Background())
	log.Println("session reconciliation initialized")

	// Create agent definition store.
	defStore := bridge.NewAgentDefStore(dbpool)

	// Create workflow definition store.
	workflowStore := bridge.NewWorkflowStore(dbpool)

	// Create workflow engine.
	workflowEngine := bridge.NewWorkflowEngine(dbpool, dispatcher, workflowStore, defStore, credStore)
	dispatcher.SetWorkflowEngine(workflowEngine)

	// Recover running workflows after Bridge restart.
	if err := workflowEngine.RecoverWorkflows(context.Background()); err != nil {
		log.Printf("error recovering workflows: %v", err)
	}
	log.Println("workflow engine initialized")

	// Create and start the scheduler.
	scheduler := bridge.NewScheduler(dbpool, dispatcher, cfg, credStore, defStore, settingsStore)
	scheduler.SetWorkflowEngine(workflowEngine)
	scheduler.Start(context.Background())
	defer scheduler.Stop()

	// Create agent repo syncer.
	syncer := bridge.NewAgentRepoSyncer(dbpool, settingsStore, scheduler, defStore, dispatcher, profileStore, workflowStore)
	syncer.Start(context.Background())
	defer syncer.Stop()
	log.Println("agent repo syncer started")

	teamStore := bridge.NewTeamStore(dbpool)
	api := bridge.NewAPI(dispatcher, dbpool, cfg, scheduler, credStore, toolStore, profileStore, settingsStore, bridgeLLM, defStore, syncer, store, workflowEngine, teamStore)

	// Build HTTP server.
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	// Auth routes.
	mux.HandleFunc("/api/v1/auth/login", auth.LoginHandler(store, mgr))
	mux.HandleFunc("/api/v1/auth/me", auth.MeHandler(cfg.AuthBackend))

	// User management API (only available with backends that support it).
	if mgr, ok := store.(auth.UserManager); ok {
		userAPI := auth.NewUserAPI(mgr)
		mux.HandleFunc("/api/v1/users", userAPI.HandleUsers)
		mux.HandleFunc("/api/v1/users/", userAPI.HandleUserByID)
		mux.HandleFunc("/api/v1/auth/password", auth.ChangeOwnPasswordHandler(mgr))
		log.Println("user management API enabled (postgres backend)")
	}

	// Dashboard static files.
	webDir := envOrDefault("ALCOVE_WEB_DIR", "web")
	if info, err := os.Stat(webDir); err == nil && info.IsDir() {
		mux.Handle("/", http.FileServer(http.Dir(webDir)))
		log.Printf("serving dashboard from %s", webDir)
	} else {
		// Fallback: return a simple JSON response for root.
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"service":"alcove-bridge","status":"ok"}`))
		})
	}

	// Wrap with auth middleware.
	handler := auth.AuthMiddleware(store, mgr, dbpool)(mux)

	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // SSE needs unbounded writes
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("listening on :%s", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server error: %v", err)
		}
	}()

	<-sigCh
	log.Println("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}

	nc.Drain()
	log.Println("shutdown complete")
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
