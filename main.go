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
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"

	"github.com/haneyeric/chirpy/internal/auth"
	"github.com/haneyeric/chirpy/internal/database"
)

const EXPIRES = 60 * 60

type apiConfig struct {
	fileserverhits atomic.Int32
	dbq            *database.Queries
	platform       string
	JWT_Secret     string
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
	Expires  int    `json:"uexpires_in_seconds"`
}

func main() {
	godotenv.Load()
	dbURL := os.Getenv("DB_URL")
	platform := os.Getenv("PLATFORM")
	jwtsecret := os.Getenv("JWT_SECRET")
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		return
	}
	dbQueries := database.New(db)
	const port = "8080"
	const filerootpath = "."
	mux := http.NewServeMux()

	cfg := apiConfig{fileserverhits: atomic.Int32{}, dbq: dbQueries, platform: platform, JWT_Secret: jwtsecret}

	mux.Handle("/app/", cfg.middlewareMetricsInc(http.StripPrefix("/app", http.FileServer(http.Dir(filerootpath)))))
	mux.HandleFunc("GET /api/healthz", healthz)
	mux.HandleFunc("GET /api/chirps", cfg.getChirps)
	mux.HandleFunc("GET /api/chirps/{chirpID}", cfg.getChirp)
	mux.HandleFunc("POST /api/chirps", cfg.createChirp)
	mux.HandleFunc("GET /admin/metrics", cfg.metrics)
	mux.HandleFunc("POST /admin/reset", cfg.reset)
	mux.HandleFunc("POST /api/users", cfg.createUser)
	mux.HandleFunc("POST /api/login", cfg.login)
	mux.HandleFunc("POST /api/refresh", cfg.refresh)
	mux.HandleFunc("POST /api/revoke", cfg.revoke)
	mux.HandleFunc("PUT /api/users", cfg.updateUser)

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
}

func (cfg *apiConfig) login(w http.ResponseWriter, r *http.Request) {
	type LoginResponse struct {
		Id           uuid.UUID `json:"id,omitempty"`
		Created_at   time.Time `json:"created_at,omitempty"`
		Updated_at   time.Time `json:"updated_at,omitempty"`
		Email        string    `json:"email,omitempty"`
		Token        string    `json:"token,omitempty"`
		RefreshToken string    `json:"refresh_token,omitempty"`
	}

	decoder := json.NewDecoder(r.Body)

	userInput := UserInput{}

	err := decoder.Decode(&userInput)

	if err != nil {
		w.WriteHeader(401)
		w.Write([]byte(("Incorrect email or password decode input")))
		return
	}

	user, err := cfg.dbq.GetUser(r.Context(), userInput.Email)
	if err != nil {
		w.WriteHeader(401)
		w.Write([]byte(("Incorrect email or password lookup")))
		return
	}
	err = auth.CheckPasswordHash(userInput.Password, user.HashedPassword)

	if err != nil {
		w.WriteHeader(401)
		w.Write([]byte(("Incorrect email or password pass check")))
		return
	}

	token, err := auth.MakeJWT(user.ID, cfg.JWT_Secret, time.Duration(EXPIRES)*time.Second)
	if err != nil {
		w.WriteHeader(401)
		w.Write([]byte(fmt.Sprintf("Incorrect email or password token creation: %s", err)))
		return
	}

	refresh, err := auth.MakeRefreshToken()

	if err != nil {
		return
	}

	params := database.CreateRefreshTokenParams{Token: refresh, UserID: user.ID}

	cfg.dbq.CreateRefreshToken(r.Context(), params)

	l := LoginResponse{Id: user.ID, Created_at: user.CreatedAt, Updated_at: user.UpdatedAt, Email: user.Email, Token: token, RefreshToken: refresh}

	body, err := json.Marshal(l)

	if err != nil {
		w.WriteHeader(500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(body)
}

func (cfg *apiConfig) refresh(w http.ResponseWriter, r *http.Request) {
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		w.WriteHeader(401)
		return
	}
	currToken, err := cfg.dbq.GetRefreshToken(r.Context(), token)
	if err != nil {
		w.WriteHeader(401)
		return
	}
	if currToken.RevokedAt.Valid || currToken.ExpiresAt.Before(time.Now()) {
		w.WriteHeader(401)
		return
	}

	user := currToken.UserID

	newToken, err := auth.MakeJWT(user, cfg.JWT_Secret, time.Duration(EXPIRES)*time.Second)
	if err != nil {
		w.WriteHeader(401)
		return
	}

	type refreshResponse struct {
		Token string `json:"token,omitempty"`
	}

	body, err := json.Marshal(refreshResponse{Token: newToken})
	if err != nil {
		w.WriteHeader(401)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(body)

}
func (cfg *apiConfig) revoke(w http.ResponseWriter, r *http.Request) {
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		w.WriteHeader(401)
		return
	}
	err = cfg.dbq.RevokeRefreshToken(r.Context(), token)
	if err != nil {
		w.WriteHeader(401)
		return
	}
	w.WriteHeader(204)

}

func (cfg *apiConfig) updateUser(w http.ResponseWriter, r *http.Request) {
	token, err := auth.GetBearerToken(r.Header)

	if err != nil {
		w.WriteHeader(401)
		return
	}

	id, err := auth.ValidateJWT(token, cfg.JWT_Secret)
	if err != nil {
		w.WriteHeader(401)
		return
	}

	decoder := json.NewDecoder(r.Body)

	userInput := UserInput{}

	err = decoder.Decode(&userInput)

	if err != nil {
		w.WriteHeader(500)
		return
	}

	hashed, err := auth.HashedPassword(userInput.Password)
	if err != nil {
		w.WriteHeader(500)
		return
	}

	params := database.UpdateUserParams{ID: id, Email: userInput.Email, HashedPassword: hashed}
	newUser, err := cfg.dbq.UpdateUser(r.Context(), params)
	if err != nil {
		w.WriteHeader(500)
		return
	}
	newUser.HashedPassword = ""
	body, err := json.Marshal(newUser)

	if err != nil {
		w.WriteHeader(500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(body)

}

func (cfg *apiConfig) createUser(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)

	userInput := UserInput{}

	err := decoder.Decode(&userInput)

	if err != nil {
		w.WriteHeader(500)
		return
	}

	hashed, err := auth.HashedPassword(userInput.Password)
	if err != nil {
		w.WriteHeader(500)
		return
	}

	params := database.CreateUserParams{Email: userInput.Email, HashedPassword: hashed}

	user, err := cfg.dbq.CreateUser(r.Context(), params)

	if err != nil {
		return
	}
	user.HashedPassword = ""
	body, err := json.Marshal(user)

	if err != nil {
		w.WriteHeader(500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	w.Write(body)
}
func (cfg *apiConfig) createChirp(w http.ResponseWriter, r *http.Request) {
	token, err := auth.GetBearerToken(r.Header)

	if err != nil {
		w.WriteHeader(401)
		return
	}

	id, err := auth.ValidateJWT(token, cfg.JWT_Secret)
	if err != nil {
		w.WriteHeader(401)
		return
	}

	decoder := json.NewDecoder(r.Body)
	chirp := database.Chirp{}
	err = decoder.Decode(&chirp)

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

	params := database.CreateChirpParams{Body: chirp.Body, UserID: id}
	chirp, err = cfg.dbq.CreateChirp(r.Context(), params)

	if err != nil {
		fmt.Printf("Create chirp: %s", err)
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
}
func (cfg *apiConfig) reset(w http.ResponseWriter, r *http.Request) {
	if cfg.platform != "dev" {
		w.WriteHeader(403)
		return
	}
	cfg.dbq.DeleteUsers(r.Context())
	cfg.dbq.DeleteChirps(r.Context())
	cfg.dbq.DeleteRefreshTokens(r.Context())
	cfg.fileserverhits.Store(0)
	w.Header().Add("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf("Hits: %d", cfg.fileserverhits.Load())))
}
