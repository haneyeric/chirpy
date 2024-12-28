package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/haneyeric/chirpy/internal/auth"
	"github.com/haneyeric/chirpy/internal/database"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

type apiConfig struct {
	fileserverhits atomic.Int32
	dbq            *database.Queries
	platform       string
}

type Chirp struct {
	Body   string `json:"body"`
	UserId string `json:"user_id"`
}

type ChirpResponse struct {
	CleanedBody string `json:"cleaned_body"`
	Valid       bool   `json:"valid"`
	Error       string `json:"error"`
}

type UserInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func main() {
	godotenv.Load()
	dbURL := os.Getenv("DB_URL")
	platform := os.Getenv("PLATFORM")
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		return
	}
	dbQueries := database.New(db)
	const port = "8080"
	const filerootpath = "."
	mux := http.NewServeMux()

	cfg := apiConfig{fileserverhits: atomic.Int32{}, dbq: dbQueries, platform: platform}

	mux.Handle("/app/", cfg.middlewareMetricsInc(http.StripPrefix("/app", http.FileServer(http.Dir(filerootpath)))))
	mux.HandleFunc("GET /api/healthz", healthz)
	mux.HandleFunc("GET /api/chirps", cfg.getChirps)
	mux.HandleFunc("GET /api/chirps/{chirpID}", cfg.getChirp)
	mux.HandleFunc("POST /api/chirps", cfg.createChirp)
	mux.HandleFunc("GET /admin/metrics", cfg.metrics)
	mux.HandleFunc("POST /admin/reset", cfg.reset)
	mux.HandleFunc("POST /api/users", cfg.createUser)
	mux.HandleFunc("POST /api/login", cfg.login)

	server := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}
	log.Printf("Serving from %s on port: %s\n", filerootpath, port)
	log.Fatal(server.ListenAndServe())
}

func healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(http.StatusText(http.StatusOK)))
	return
}

func (cfg *apiConfig) getChirp(w http.ResponseWriter, r *http.Request) {
	cid, err := uuid.Parse(r.PathValue("chirpID"))

	if err != nil {
		w.WriteHeader(404)
		return
	}

	chirp, err := cfg.dbq.GetChirp(r.Context(), cid)

	if err != nil {
		w.WriteHeader(404)
		return
	}
	body, err := json.Marshal(chirp)

	if err != nil {
		w.WriteHeader(404)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(body)
	return
}

func (cfg *apiConfig) getChirps(w http.ResponseWriter, r *http.Request) {
	chirps, err := cfg.dbq.GetChirps(r.Context())
	if err != nil {
		w.WriteHeader(400)
		return
	}
	body, err := json.Marshal(chirps)

	if err != nil {
		w.WriteHeader(500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(body)
	return
}

func (cfg *apiConfig) login(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)

	userInput := UserInput{}

	err := decoder.Decode(&userInput)

	if err != nil {
		w.WriteHeader(401)
		w.Write([]byte(fmt.Sprintf("Incorrect email or password decode input")))
		return
	}

	user, err := cfg.dbq.GetUser(r.Context(), userInput.Email)
	if err != nil {
		w.WriteHeader(401)
		w.Write([]byte(fmt.Sprintf("Incorrect email or password lookup")))
		return
	}
	err = auth.CheckPasswordHash(userInput.Password, user.HashedPassword)

	if err != nil {
		w.WriteHeader(401)
		w.Write([]byte(fmt.Sprintf("Incorrect email or password pass check")))
		return
	}

	user.HashedPassword = ""
	body, err := json.Marshal(user)

	if err != nil {
		w.WriteHeader(500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(body)
	return
}
func (cfg *apiConfig) createUser(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)

	userInput := UserInput{}

	err := decoder.Decode(&userInput)

	fmt.Printf("userInput.email: %s\n", userInput.Email)
	fmt.Printf("userInput.password: %s\n", userInput.Password)

	if err != nil {
		w.WriteHeader(500)
		return
	}

	hashed, err := auth.HashedPassword(userInput.Password)
	if err != nil {
		w.WriteHeader(500)
		return
	}

	fmt.Printf("hashed: %s\n", hashed)

	params := database.CreateUserParams{Email: userInput.Email, HashedPassword: hashed}

	user, err := cfg.dbq.CreateUser(r.Context(), params)

	if err != nil {
		return
	}
	user.HashedPassword = ""
	body, err := json.Marshal(user)

	fmt.Printf("user.Email: %s\n", user.Email)
	fmt.Printf("body: %s\n", body)

	if err != nil {
		w.WriteHeader(500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	w.Write(body)
	return
}
func (cfg *apiConfig) createChirp(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)
	chirp := database.Chirp{}
	err := decoder.Decode(&chirp)

	if err != nil {
		w.WriteHeader(500)
		return
	}

	if len(chirp.Body) > 140 {
		b := ChirpResponse{Error: "Chirp is too long"}
		body, err := json.Marshal(b)

		if err != nil {
			w.WriteHeader(500)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		w.Write(body)
		return
	}

	words := strings.Split(chirp.Body, " ")
	profanes := []string{"kerfuffle", "sharbert", "fornax"}

	for i := 0; i < len(words); i++ {
		for j := 0; j < len(profanes); j++ {
			if strings.ToLower(words[i]) == profanes[j] {
				words[i] = "****"
				break
			}
		}
	}
	s := ChirpResponse{CleanedBody: strings.Join(words, " ")}
	chirp.Body = s.CleanedBody

	params := database.CreateChirpParams{Body: chirp.Body, UserID: chirp.UserID}
	chirp, err = cfg.dbq.CreateChirp(r.Context(), params)

	if err != nil {
		w.WriteHeader(400)
		return
	}

	body, err := json.Marshal(chirp)

	if err != nil {
		w.WriteHeader(500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	w.Write(body)
	return
}

func (cfg *apiConfig) middlewareMetricsInc(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg.fileserverhits.Add(1)
		next.ServeHTTP(w, r)
	})
}

func (cfg *apiConfig) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf(`
	<html>
	  <body>
		<h1>Welcome, Chirpy Admin</h1>
		<p>Chirpy has been visited %d times!</p>
	  </body>
	</html>
	`, cfg.fileserverhits.Load())))
	return
}
func (cfg *apiConfig) reset(w http.ResponseWriter, r *http.Request) {
	if cfg.platform != "dev" {
		w.WriteHeader(403)
		return
	}
	cfg.dbq.DeleteUsers(r.Context())
	cfg.fileserverhits.Store(0)
	w.Header().Add("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf("Hits: %d", cfg.fileserverhits.Load())))
	return
}
