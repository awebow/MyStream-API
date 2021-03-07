package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/locales/en"
	ut "github.com/go-playground/universal-translator"
	"github.com/go-playground/validator/v10"
)

func (app *App) InitValidTrans() {
	app.validTrans, _ = ut.New(en.New()).GetTranslator("en")

	if validate, ok := binding.Validator.Engine().(*validator.Validate); ok {
		validate.RegisterTagNameFunc(func(fld reflect.StructField) string {
			name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
			if name == "-" {
				return ""
			}
			return name
		})

		validate.RegisterTranslation("required", app.validTrans, func(ut ut.Translator) error {
			return ut.Add("required", "field '{0}' is missing", true)
		}, func(ut ut.Translator, fe validator.FieldError) string {
			t, _ := ut.T("required", fe.Field())
			return t
		})

		validate.RegisterTranslation("min", app.validTrans, func(ut ut.Translator) error {
			return ut.Add("min", "field '{0}' must be at least {1} characters", true)
		}, func(ut ut.Translator, fe validator.FieldError) string {
			t, _ := ut.T("min", fe.Field(), fe.Param())
			return t
		})

		validate.RegisterTranslation("max", app.validTrans, func(ut ut.Translator) error {
			return ut.Add("max", "field '{0}' must be at most {1} characters", true)
		}, func(ut ut.Translator, fe validator.FieldError) string {
			t, _ := ut.T("max", fe.Field(), fe.Param())
			return t
		})

		validate.RegisterTranslation("email", app.validTrans, func(ut ut.Translator) error {
			return ut.Add("email", "'{0}' is not a valid email address", true)
		}, func(ut ut.Translator, fe validator.FieldError) string {
			t, _ := ut.T("email", fe.Value().(string))
			return t
		})
	}
}

func (app *App) HandleError(c *gin.Context, err error) {
	if v, ok := err.(validator.ValidationErrors); ok {
		messages := []string{}
		for _, msg := range v.Translate(app.validTrans) {
			messages = append(messages, msg)
		}

		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"message": strings.Join(messages, "\n")})
	} else if v, ok := err.(*json.UnmarshalTypeError); ok {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("type error on field '%s'", v.Field)})
	} else {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
	}
}
