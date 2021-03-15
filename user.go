package main

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/lestrrat-go/jwx/jwa"
	"github.com/lestrrat-go/jwx/jwt"
	"github.com/oklog/ulid/v2"
	"golang.org/x/crypto/sha3"
)

type User struct {
	ID            string     `json:"id" db:"id"`
	Email         string     `json:"email" db:"email"`
	Name          string     `json:"name" db:"name"`
	Picture       *string    `json:"picture" db:"picture"`
	RegisterdAt   time.Time  `json:"registered_at" db:"registered_at"`
	DeactivatedAt *time.Time `json:"deactivated_at" db:"deactivated_at"`
}

func (app *App) SelectUser(id string) (u *User, err error) {
	u = &User{}
	var rows *sqlx.Rows
	rows, err = app.db.Unsafe().Queryx("SELECT * FROM users WHERE `id`=?", id)
	if err != nil {
		return
	}

	if rows.Next() {
		err = rows.StructScan(u)
	} else {
		err = NotFoundError("user")
	}
	return
}

func (app *App) GetMe(c *gin.Context) {
	me, err := app.SelectUser(c.GetString("UserID"))
	if err != nil {
		if v, ok := err.(*HTTPError); ok && v.StatusCode == http.StatusNotFound {
			err = AuthorizationError()
		}

		app.HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, me)
}

func (app *App) GetMyChannels(c *gin.Context) {
	rows, err := app.db.Unsafe().Queryx("SELECT * FROM channels WHERE `owner`=?", c.GetString("UserID"))
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

func (app *App) PostUser(c *gin.Context) {
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
			app.HandleError(c, &HTTPError{http.StatusConflict, "the e-mail is already registered"})
		} else {
			app.HandleError(c, err)
		}

		tx.Rollback()
		return
	}

	stmt, err := tx.Prepare("UPDATE users SET `id`=? WHERE `email`=?")
	if err != nil {
		tx.Rollback()
		app.HandleError(c, err)
		return
	}

	for i := 0; i < app.Config.ULIDConflictRetry+1; i++ {
		id = ulid.MustNew(ulid.Timestamp(now), entropy)

		_, err = stmt.Exec(id.String(), body.Email)

		if err == nil {
			break
		}
	}

	if stmt.Close() == nil && err == nil {
		tx.Commit()
		c.JSON(http.StatusOK, gin.H{"id": id})
	} else {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"msg": "Unknown server error."})
	}
}

func (app *App) PutUserPicture(c *gin.Context) {
	userID := c.GetString("UserID")

	header, err := c.FormFile("file")
	if err != nil {
		app.HandleError(c, err)
		return
	}

	file, err := header.Open()
	if err != nil {
		app.HandleError(c, err)
		return
	}
	defer file.Close()

	img, err := imaging.Decode(file)
	if err != nil {
		app.HandleError(c, err)
		return
	}

	dir, err := ioutil.TempDir("", "picture")
	if err != nil {
		app.HandleError(c, err)
		return
	}
	defer os.RemoveAll(dir)

	for _, o := range app.Config.UserPicture {
		output, err := os.Create(fmt.Sprintf("%s/%dx%d.jpg", dir, o.Width, o.Height))
		if err != nil {
			app.HandleError(c, err)
			return
		}

		resized := imaging.Fill(img, o.Width, o.Height, imaging.Center, imaging.Lanczos)
		err = imaging.Encode(output, resized, imaging.JPEG, imaging.JPEGQuality(app.Config.Thumbnail.Quality))
		if err != nil {
			output.Close()
			app.HandleError(c, err)
			return
		}

		if err = output.Close(); err != nil {
			app.HandleError(c, err)
			return
		}
	}

	now := time.Now()
	entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
	fileName := "u" + userID + ulid.MustNew(ulid.Timestamp(now), entropy).String()

	if err = app.StoreFile(dir, "images/"+fileName); err != nil {
		app.HandleError(c, err)
		return
	}

	_, err = app.db.Query("UPDATE users SET `picture`=? WHERE `id`=?", fileName, userID)
	if err != nil {
		app.HandleError(c, err)
		return
	}

	me, err := app.SelectUser(userID)
	if err != nil {
		app.HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, me)
}

func (app *App) PostToken(c *gin.Context) {
	body := struct {
		Email    string `json:"email" binding:"required"`
		Password string `json:"password" binding:"required"`
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
		app.HandleError(c, AuthorizationError())
	}
}

func (app *App) AuthMiddleware(allowUnauth bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		s := strings.Split(c.GetHeader("Authorization"), " ")
		if len(s) == 2 && s[0] == "Bearer" {
			token, err := jwt.Parse([]byte(s[1]), jwt.WithVerify(jwa.HS256, []byte(app.Config.AuthSignKey)))
			if err == nil {
				if id, ok := token.Get("user_id"); ok {
					c.Set("UserID", id)
					c.Next()
					return
				}
			}
		}

		if allowUnauth {
			c.Next()
		} else {
			app.HandleError(c, AuthorizationError())
		}
	}
}

func hashPassword(email string, password string) []byte {
	hashed := sha3.Sum256([]byte(email + password))
	return hashed[:]
}
