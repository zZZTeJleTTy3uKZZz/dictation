// Package routecontract хранит таблицу вызовов окна и сверяет её с маршрутами
// службы. Проверка кроссплатформенная: она читает исходники, поэтому работает
// и на сборочном Linux, где internal/localdict не собирается из-за Windows API.
package routecontract
