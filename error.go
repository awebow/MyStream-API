package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-playground/locales/en"
	ut "github.com/go-playground/universal-translator"
	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
)

func NotFoundError(resource string) error {
	return echo.NewHTTPError(http.StatusNotFound, "can not find the "+resource)
}

type Validator struct {
	validator  *validator.Validate
	translator ut.Translator
}

func (v *Validator) Validate(i interface{}) error {
	err := v.validator.Struct(i)
	if err == nil {
		return nil
	}

	if ve, ok := err.(validator.ValidationErrors); ok {
		messages := []string{}
		for _, msg := range ve.Translate(v.translator) {
			messages = append(messages, msg)
		}

		return echo.NewHTTPError(http.StatusBadRequest, strings.Join(messages, "\n"))
	}

	return echo.NewHTTPError(http.StatusBadRequest, err.Error())
}

func InitValidTrans(e *echo.Echo) {
	v := &Validator{validator: validator.New()}
	v.translator, _ = ut.New(en.New()).GetTranslator("en")
	e.Validator = v

	v.validator.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "-" {
			return ""
		}
		return name
	})

	v.validator.RegisterTranslation("required", v.translator, func(ut ut.Translator) error {
		return ut.Add("required", "field '{0}' is missing", true)
	}, func(ut ut.Translator, fe validator.FieldError) string {
		t, _ := ut.T("required", fe.Field())
		return t
	})

	v.validator.RegisterTranslation("min", v.translator, func(ut ut.Translator) error {
		return ut.Add("min", "field '{0}' must be at least {1} characters", true)
	}, func(ut ut.Translator, fe validator.FieldError) string {
		t, _ := ut.T("min", fe.Field(), fe.Param())
		return t
	})

	v.validator.RegisterTranslation("max", v.translator, func(ut ut.Translator) error {
		return ut.Add("max", "field '{0}' must be at most {1} characters", true)
	}, func(ut ut.Translator, fe validator.FieldError) string {
		t, _ := ut.T("max", fe.Field(), fe.Param())
		return t
	})

	v.validator.RegisterTranslation("email", v.translator, func(ut ut.Translator) error {
		return ut.Add("email", "'{0}' is not a valid email address", true)
	}, func(ut ut.Translator, fe validator.FieldError) string {
		t, _ := ut.T("email", fe.Value().(string))
		return t
	})
}

func (app *App) ErrorHandler(err error, c echo.Context) {
	if v, ok := err.(*json.UnmarshalTypeError); ok {
		err = echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("type error on field '%s'", v.Field))
	}

	c.Echo().DefaultHTTPErrorHandler(err, c)
}
