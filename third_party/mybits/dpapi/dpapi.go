// Package dpapi — шифрование секретов, привязанное к учётке Windows
// (DPAPI, CurrentUser): файл с токеном, унесённый на другую машину или к
// другому пользователю, не расшифровывается. CRYPTPROTECT_LOCAL_MACHINE
// намеренно не используется.
//
// Вне Windows — ErrUnsupported: там секреты держат в связке ключей
// (Secret Service, Keychain) или в файле 0600.
package dpapi

import "errors"

// ErrUnsupported — DPAPI есть только на Windows.
var ErrUnsupported = errors.New("dpapi: только на Windows")

// Protect шифрует data для текущего пользователя.
func Protect(data []byte) ([]byte, error) { return crypt(data, false) }

// Unprotect расшифровывает то, что зашифровал Protect под этим же пользователем.
func Unprotect(data []byte) ([]byte, error) { return crypt(data, true) }

// Supported — работает ли DPAPI на этой системе.
func Supported() bool { return supported }
