package main

import (
	"errors"
	"fmt"

	mid "maunium.net/go/mautrix/id"
)

func GetEmailForUser(userID mid.UserID) (string, error) {
	switch userID.String() {
	case "@sumner:localhost":
		return "me@sumnerevans.com", nil
	}

	return "", errors.New(fmt.Sprintf("Couldn't find email for user %s", userID))
}
