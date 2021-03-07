package main

import (
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"github.com/lestrrat-go/jwx/jwa"
	"github.com/lestrrat-go/jwx/jwt"
	"github.com/oklog/ulid/v2"
	"golang.org/x/crypto/sha3"
)

type User struct {
	ID            string     `json:"id" db:"id"`
	Email         string     `json:"email" db:"email"`
	Name          string     `json:"name" db:"name"`
	RegisterdAt   time.Time  `json:"registered_at" db:"registered_at"`
	DeactivatedAt *time.Time `json:"deactivated_at" db:"deactivated_at"`
}

func (app *App) GetMe(c *gin.Context) {
	sql := "SELECT `id`, `email`, `name`, `registered_at`, `deactivated_at` FROM users WHERE `id`=?"
	rows, err := app.db.Queryx(sql, c.GetString("UserID"))
	if err != nil {
		app.HandleError(c, err)
		return
	}

	if rows.Next() {
		user := User{}
		rows.StructScan(&user)
		c.JSON(http.StatusOK, user)
	} else {
		c.AbortWithStatus(http.StatusUnauthorized)
	}
}

func (app *App) GetMyChannels(c *gin.Context) {
	sql := "SELECT `id`, `name`, `description`, `created_at`, `deactivated_at` FROM channels WHERE `owner`=?"
	rows, err := app.db.Queryx(sql, c.GetString("UserID"))
	if err != nil {
		app.HandleError(c, err)
		return
	}

	channels := []Channel{}
	for rows.Next() {
		channel := Channel{}
		rows.StructScan(&channel)
		channels = append(channels, channel)
	}

	c.JSON(http.StatusOK, channels)
}

func (app *App) PostUsers(c *gin.Context) {
	body := struct {
		Email    string `json:"email" binding:"email,required,max=255"`
		Password string `json:"password" binding:"required,min=8,max=255"`
		Name     string `json:"name" binding:"required,max=64"`
	}{}
	if err := c.ShouldBindJSON(&body); err != nil {
		app.HandleError(c, err)
		return
	}

	now := time.Now()
	entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)

	var id ulid.ULID
	tx, err := app.db.Beginx()
	if err != nil {
		app.HandleError(c, err)
		return
	}

	_, err = tx.Exec("INSERT INTO users (`id`, `email`, `password`, `name`, `registered_at`) VALUES (?, ?, ?, ?, ?)",
		body.Email,
		body.Email,
		hashPassword(body.Email, body.Password),
		body.Name,
		now,
	)

	if err != nil {
		if v, ok := err.(*mysql.MySQLError); ok && v.Number == 1062 {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"message": "the e-mail is already registered"})
		} else {
			app.HandleError(c, err)
		}

		tx.Rollback()
		return
	}

	stmt, err := tx.Prepare("UPDATE users SET `id`=? WHERE `email`=?")

	inserted := false
	for i := 0; i < app.Config.ULIDConflictRetry+1; i++ {
		id = ulid.MustNew(ulid.Timestamp(now), entropy)

		_, err = stmt.Exec(id.String(), body.Email)

		if err == nil {
			inserted = true
			break
		}
	}

	if inserted {
		tx.Commit()
		c.JSON(http.StatusOK, gin.H{"id": id})
	} else {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"msg": "Unknown server error."})
	}
}

func (app *App) PostTokens(c *gin.Context) {
	body := struct {
		Email    string `json:"email" binding:"required"`
		Password string `json:"password" binding:"required`
	}{}
	if err := c.ShouldBindJSON(&body); err != nil {
		app.HandleError(c, err)
		return
	}

	sql := "SELECT `id` FROM users WHERE `email`=? AND `password`=?"
	rows, err := app.db.Query(sql, body.Email, hashPassword(body.Email, body.Password))
	if err != nil {
		app.HandleError(c, err)
		return
	}

	if rows.Next() {
		var id string
		rows.Scan(&id)

		token := jwt.New()
		token.Set("user_id", id)

		signed, err := jwt.Sign(token, jwa.HS256, []byte(app.Config.AuthSignKey))
		if err != nil {
			app.HandleError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"token": string(signed)})
	} else {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "wrong e-mail or password"})
	}
}

func (app *App) AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		s := strings.Split(c.GetHeader("Authorization"), " ")
		if len(s) == 2 && s[0] == "Bearer" {
			token, err := jwt.Parse([]byte(s[1]), jwt.WithVerify(jwa.HS256, []byte(app.Config.AuthSignKey)))
			if err == nil {
				id, ok := token.Get("user_id")
				if ok {
					c.Set("UserID", id)
					c.Next()
					return
				}
			}
		}

		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "authorization failed"})
	}
}

func hashPassword(email string, password string) []byte {
	hashed := sha3.Sum256([]byte(email + password))
	return hashed[:]
}
