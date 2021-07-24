package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPassword(t *testing.T) {
	salted := hashPassword("testpa$sW0rd")
	assert.True(t, verifyPassword("testpa$sW0rd", salted))
}
