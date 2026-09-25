// Copyright 2026 ETH Zurich
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

package webapp

import (
	"crypto/rand"
	"embed"
	"encoding/gob"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt"
	"github.com/gorilla/sessions"
	"golang.org/x/crypto/bcrypt"

	"github.com/scionproto/scion/marketplace/db"
	"github.com/scionproto/scion/marketplace/storage"
	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/hummingbird/registration"
	"github.com/scionproto/scion/pkg/log"
)

// The templates and the static assets are embedded, so that the marketplace does
// not depend on being started from the root of the repository.
//
//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

var templates = template.Must(template.ParseFS(templateFS, "templates/*.html"))

func Init(signer *registration.Signer, store *storage.MarketplaceStorage, mux *http.ServeMux, disableUserRegistration bool) {
	sessionKey := make([]byte, 32)
	rand.Read(sessionKey)
	sessionStore := sessions.NewCookieStore(sessionKey)
	sessionStore.Options = &sessions.Options{
		Secure:   true,
		HttpOnly: true,
		MaxAge:   0,
		SameSite: http.SameSiteLaxMode,
	}
	gob.Register(User{})
	gob.Register(addr.IA(0))
	h := &Handler{
		sessions:                sessionStore,
		signer:                  signer,
		store:                   store,
		disableUserRegistration: disableUserRegistration,
	}
	mux.HandleFunc("/", h.accountHandler)
	mux.HandleFunc("/login", h.loginHandler)
	mux.HandleFunc("/register", h.registerHandler)
	mux.HandleFunc("/account/balance", h.accountBalanceHandler)
	mux.HandleFunc("/account/create", h.accountCreateHandler)
	mux.HandleFunc("/account/delete", h.accountDeletionHandler)
	mux.HandleFunc("/account/token", h.accountTokenHandler)
	mux.HandleFunc("/account/resetjwt", h.accountResetJWTHandler)
	mux.HandleFunc("/account", h.accountHandler)
	mux.HandleFunc("/assets", h.assetsHandler)
	mux.HandleFunc("/assets/assign", h.assignAssetHandler)
	mux.HandleFunc("/reservations", h.reservationsHandler)
	mux.HandleFunc("/reservations/assign", h.assignReservationHandler)
	mux.HandleFunc("/logout", h.logoutHandler)
	mux.HandleFunc("/aslogin", h.asLoginHandler)
	mux.HandleFunc("/asbalance", h.asBalanceHandler)
	mux.Handle("/static/", http.FileServerFS(staticFS))
}

type Handler struct {
	sessions                sessions.Store
	store                   *storage.MarketplaceStorage
	signer                  *registration.Signer
	disableUserRegistration bool
}
type User struct {
	ID   int64
	Name string
}
type Account struct {
	ID int64
	// Scope names a sub account, and is empty for the main account. The handlers
	// fill it with the user name before rendering the main account.
	Scope        string
	Balance      int64
	TokenVersion int64
}

func (h *Handler) SetSessionUser(w http.ResponseWriter, r *http.Request, user any) error {
	session, _ := h.sessions.Get(r, "session_id")
	session.Values["user"] = user
	session.Values["iat"] = time.Now().Unix()
	return session.Save(r, w)
}

func (h *Handler) GetSession(r *http.Request) (*sessions.Session, error) {
	session, err := h.sessions.Get(r, "session_id")
	if err != nil {
		return nil, err
	}
	iat, ok := session.Values["iat"].(int64)
	if !ok {
		return nil, fmt.Errorf("session invalid")
	}
	if time.Now().Unix() > iat+3600 {
		return nil, fmt.Errorf("session invalid")
	}
	return session, nil
}

func (h *Handler) asLoginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		templates.ExecuteTemplate(w, "aslogin.html", map[string]any{})
		return
	}

	r.ParseForm()
	username := r.FormValue("username")
	password := r.FormValue("password")
	ia, err := addr.ParseIA(username)
	dbUser, err := h.store.GetASUser(r.Context(), ia)
	if err != nil {
		log.Debug("AS login handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if dbUser == nil {
		templates.ExecuteTemplate(w, "aslogin.html", map[string]any{
			"Error": "invalid credentials",
		})
		return
	}
	err = bcrypt.CompareHashAndPassword(
		[]byte(dbUser.PasswordHash),
		[]byte(password),
	)
	if err != nil {
		templates.ExecuteTemplate(w, "aslogin.html", map[string]any{
			"Error": "invalid credentials",
		})
		return
	}

	err = h.SetSessionUser(w, r, dbUser.IA)
	if err != nil {
		log.Debug("AS login handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/asbalance", http.StatusSeeOther)
}

func (h *Handler) asBalanceHandler(w http.ResponseWriter, r *http.Request) {
	session, err := h.GetSession(r)
	if err != nil {
		http.Redirect(w, r, "/aslogin", http.StatusSeeOther)
		return
	}
	ia, ok := session.Values["user"].(addr.IA)
	if !ok {
		http.Redirect(w, r, "/aslogin", http.StatusSeeOther)
		return
	}
	dbUser, err := h.store.GetASUser(r.Context(), ia)
	if err != nil {
		log.Debug("AS balance handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if dbUser == nil {
		http.Redirect(w, r, "/aslogin", http.StatusSeeOther)
		return
	}
	templates.ExecuteTemplate(w, "asbalance.html", map[string]any{
		"Balance":  strconv.Itoa(int(dbUser.Balance)),
		"Username": ia.String(),
	})
}

func (h *Handler) accountBalanceHandler(w http.ResponseWriter, r *http.Request) {
	session, err := h.GetSession(r)
	if err != nil {
		http.Error(w, "invalid session", http.StatusUnauthorized)
		return
	}
	user, ok := session.Values["user"].(User)
	if !ok {
		http.Error(w, "invalid session", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.ParseForm()
	accountFromID, err := strconv.ParseInt(r.FormValue("from"), 10, 64)
	if err != nil {
		log.Debug("User account balance handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	accountToID, err := strconv.ParseInt(r.FormValue("to"), 10, 64)
	if err != nil {
		log.Debug("User account balance handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	amount, err := strconv.ParseInt(r.FormValue("amount"), 10, 64)
	if err != nil {
		log.Debug("User account balance handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, err = h.store.TransferMoneyBetweenAccounts(r.Context(), user.ID, accountFromID, accountToID, amount)
	if err != nil {
		log.Debug("User account balance handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) accountTokenHandler(w http.ResponseWriter, r *http.Request) {
	session, err := h.GetSession(r)
	if err != nil {
		http.Error(w, "invalid session", http.StatusUnauthorized)
		return
	}
	user, ok := session.Values["user"].(User)
	if !ok {
		http.Error(w, "invalid session", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.ParseForm()
	accountIDString := r.FormValue("id")
	accountID, err := strconv.ParseInt(accountIDString, 10, 64)
	if err != nil {
		log.Debug("User account token handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dbAccount, err := h.store.GetAccountByAccountID(r.Context(), accountID)
	if err != nil {
		log.Debug("User account token handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if dbAccount.UserID != user.ID {
		log.Debug("User account token handler. Account does not belong to user", "dbAccount.UserID", dbAccount.UserID, "user.ID", user.ID)
		http.Error(w, "invalid account", http.StatusUnauthorized)
		return
	}
	token, err := h.createToken(strconv.FormatInt(dbAccount.ID, 10), dbAccount.TokenVersion)
	if err != nil {
		log.Debug("User token handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(token))
}

func (h *Handler) accountDeletionHandler(w http.ResponseWriter, r *http.Request) {
	session, err := h.GetSession(r)
	if err != nil {
		http.Error(w, "invalid session", http.StatusUnauthorized)
		return
	}
	user, ok := session.Values["user"].(User)
	if !ok {
		http.Error(w, "invalid session", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.ParseForm()
	accountId, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		log.Debug("User account deletion handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, err = h.store.DeleteAccount(r.Context(), user.ID, accountId)
	if err != nil {
		log.Debug("User account creation handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) accountResetJWTHandler(w http.ResponseWriter, r *http.Request) {
	session, err := h.GetSession(r)
	if err != nil {
		http.Error(w, "invalid session", http.StatusUnauthorized)
		return
	}
	user, ok := session.Values["user"].(User)
	if !ok {
		http.Error(w, "invalid session", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.ParseForm()
	accountId, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		log.Debug("User account jwt reset handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, err = h.store.IncrementUserJWTVersion(r.Context(), user.ID, accountId)
	if err != nil {
		log.Debug("User account jwt reset handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

var accountScopeRegex = regexp.MustCompile(`^[A-Za-z0-9_-]{1,20}$`)

func (h *Handler) accountCreateHandler(w http.ResponseWriter, r *http.Request) {
	session, err := h.GetSession(r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	user, ok := session.Values["user"].(User)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	accountScope := r.FormValue("name")
	if !accountScopeRegex.MatchString(accountScope) {
		http.Error(w, "invalid name", http.StatusBadRequest)
		return
	}
	_, err = h.store.CreateAccount(r.Context(), &db.DBAccount{
		UserID: user.ID,
		Scope:  accountScope,
	})
	if err != nil {
		log.Debug("User account creation handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

func (h *Handler) assignReservationHandler(w http.ResponseWriter, r *http.Request) {
	session, err := h.GetSession(r)
	if err != nil {
		http.Error(w, "invalid session", http.StatusUnauthorized)
		return
	}
	user, ok := session.Values["user"].(User)
	if !ok {
		http.Error(w, "invalid session", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.ParseForm()
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		log.Debug("User assign reservation handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fromAccountId, err := strconv.ParseInt(r.FormValue("from"), 10, 64)
	if err != nil {
		log.Debug("User assign reservation handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	toAccountId, err := strconv.ParseInt(r.FormValue("to"), 10, 64)
	if err != nil {
		log.Debug("User assign reservation handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, err = h.store.AssignReservation(r.Context(), id, user.ID, fromAccountId, toAccountId)
	if err != nil {
		log.Debug("User assign reservation handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) assignAssetHandler(w http.ResponseWriter, r *http.Request) {
	session, err := h.GetSession(r)
	if err != nil {
		http.Error(w, "invalid session", http.StatusUnauthorized)
		return
	}
	user, ok := session.Values["user"].(User)
	if !ok {
		http.Error(w, "invalid session", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.ParseForm()
	assetId, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		log.Debug("User assign asset handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fromAccountId, err := strconv.ParseInt(r.FormValue("from"), 10, 64)
	if err != nil {
		log.Debug("User assign asset handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	toAccountId, err := strconv.ParseInt(r.FormValue("to"), 10, 64)
	if err != nil {
		log.Debug("User assign asset handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, err = h.store.AssignAsset(r.Context(), assetId, user.ID, fromAccountId, toAccountId)
	if err != nil {
		log.Debug("User assign asset handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) assetsHandler(w http.ResponseWriter, r *http.Request) {
	session, err := h.GetSession(r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	user, ok := session.Values["user"].(User)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	accountIdFilterString := r.FormValue("id")
	var accountIdFilter int64
	if accountIdFilterString != "" {
		accountIdFilter, err = strconv.ParseInt(accountIdFilterString, 10, 64)
		if err != nil {
			log.Debug("User assign asset handler", "err", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	dbAccounts, err := h.store.GetAccountsByUser(r.Context(), user.ID)
	if err != nil {
		log.Debug("User assets handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var filterAccount *db.DBAccount
	var mainAccount *db.DBAccount
	for _, a := range dbAccounts {
		if a.Scope == "" {
			a.Scope = user.Name
			mainAccount = a
		}
		if accountIdFilterString != "" && a.ID == accountIdFilter {
			filterAccount = a
		}
	}
	if filterAccount == nil {
		filterAccount = mainAccount
	}
	if mainAccount == nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	assets, err := h.store.Search(r.Context(), &db.AssetQuery{
		AccountId: &filterAccount.ID,
		Page:      0,
		PageSize:  1024, //TODO: add pagination also for GUI website
	})
	if err != nil {
		log.Debug("User assets handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	templates.ExecuteTemplate(w, "assets.html", map[string]any{
		"Username": user.Name,
		"Assets":   assets,
		"Accounts": dbAccounts,
		"Account":  filterAccount,
	})
}

func (h *Handler) reservationsHandler(w http.ResponseWriter, r *http.Request) {
	session, err := h.GetSession(r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	user, ok := session.Values["user"].(User)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	accountIdFilterString := r.FormValue("id")
	var accountIdFilter int64
	if accountIdFilterString != "" {
		accountIdFilter, err = strconv.ParseInt(accountIdFilterString, 10, 64)
		if err != nil {
			log.Debug("User assign asset handler", "err", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	dbAccounts, err := h.store.GetAccountsByUser(r.Context(), user.ID)
	if err != nil {
		log.Debug("User assets handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var filterAccount *db.DBAccount
	var mainAccount *db.DBAccount
	for _, a := range dbAccounts {
		if a.Scope == "" {
			a.Scope = user.Name
			mainAccount = a
		}
		if accountIdFilterString != "" && a.ID == accountIdFilter {
			filterAccount = a
		}
	}
	if filterAccount == nil {
		filterAccount = mainAccount
	}
	if mainAccount == nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	reservations, err := h.store.FetchReservations(r.Context(), &db.ReservationQuery{
		AccountId: &filterAccount.ID,
	})
	if err != nil {
		log.Debug("User assets handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	templates.ExecuteTemplate(w, "reservations.html", map[string]any{
		"Username":     user.Name,
		"Reservations": reservations,
		"Accounts":     dbAccounts,
		"Account":      filterAccount,
	})
}

func (h *Handler) accountHandler(w http.ResponseWriter, r *http.Request) {
	session, err := h.GetSession(r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	user, ok := session.Values["user"].(User)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	accounts, err := h.store.GetAccountsByUser(r.Context(), user.ID)
	if err != nil {
		log.Debug("User account handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	accs := []Account{}
	mainAccount := Account{}
	for _, a := range accounts {
		if a.Scope == "" {
			mainAccount = Account{
				ID:           a.ID,
				Balance:      a.Balance,
				TokenVersion: a.TokenVersion,
			}
		} else {
			accs = append(accs, Account{
				ID:           a.ID,
				Scope:        a.Scope,
				Balance:      a.Balance,
				TokenVersion: a.TokenVersion,
			})
		}

	}
	templates.ExecuteTemplate(w, "account.html", map[string]any{
		"Username": user.Name,
		"Main":     mainAccount,
		"Accounts": accs,
	})
}

func (h *Handler) registerHandler(w http.ResponseWriter, r *http.Request) {
	if h.disableUserRegistration {
		templates.ExecuteTemplate(w, "register.html", map[string]any{
			"Error": "User registration disabled",
		})
		return
	}
	if r.Method == http.MethodGet {
		templates.ExecuteTemplate(w, "register.html", map[string]any{})
		return
	}

	r.ParseForm()
	username := r.FormValue("username")
	password := r.FormValue("password")
	if !accountScopeRegex.MatchString(username) {
		templates.ExecuteTemplate(w, "register.html", map[string]any{
			"Error": "Invalid username",
		})
		return
	}
	user, err := h.store.GetUserByName(r.Context(), username)
	if err != nil {
		log.Debug("User register handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if user != nil {
		templates.ExecuteTemplate(w, "register.html", map[string]any{
			"Error": "Username already exists",
		})
		return
	}
	hash, err := bcrypt.GenerateFromPassword(
		[]byte(password),
		12,
	)
	if err != nil {
		log.Debug("User register handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	userId, err := h.store.CreateUser(r.Context(), &db.DBUser{
		Name:         username,
		PasswordHash: string(hash),
	})
	if err != nil {
		log.Debug("User register handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err = h.SetSessionUser(w, r, User{
		ID:   userId,
		Name: username,
	})
	if err != nil {
		log.Debug("User register handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

func (h *Handler) logoutHandler(w http.ResponseWriter, r *http.Request) {
	session, err := h.GetSession(r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	clear(session.Values)
	session.Options.MaxAge = -1
	err = session.Save(r, w)
	if err != nil {
		log.Debug("Logout handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *Handler) loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		templates.ExecuteTemplate(w, "login.html", map[string]any{})
		return
	}

	r.ParseForm()
	username := r.FormValue("username")
	password := r.FormValue("password")

	dbUser, err := h.store.GetUserByName(r.Context(), username)
	if err != nil {
		log.Debug("User login handler", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if dbUser == nil {
		templates.ExecuteTemplate(w, "login.html", map[string]any{
			"Error": "invalid credentials",
		})
		return
	}
	err = bcrypt.CompareHashAndPassword(
		[]byte(dbUser.PasswordHash),
		[]byte(password),
	)
	if err != nil {
		templates.ExecuteTemplate(w, "login.html", map[string]any{
			"Error": "invalid credentials",
		})
		return
	}

	err = h.SetSessionUser(w, r, User{
		ID:   dbUser.ID,
		Name: username,
	})
	if err != nil {
		templates.ExecuteTemplate(w, "login.html", map[string]any{
			"Error": err.Error(),
		})
		return
	}

	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

func (h *Handler) createToken(user string, tokenVersion int64) (string, error) {
	claims := jwt.MapClaims{
		"sub":   user,
		"scope": "User",
		"exp":   time.Now().Add(time.Hour * 24 * 7).Unix(),
		"iat":   time.Now().Unix(),
		"ver":   tokenVersion,
	}
	return h.signer.GenerateToken(claims)
}
