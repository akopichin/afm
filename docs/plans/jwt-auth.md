# JWT Middleware & Auth Endpoints

## Overview

Реализовать JWT-авторизацию с нуля. Проект сейчас — CLI-утилита без HTTP-слоя. Необходимо:
- Создать HTTP-сервер с роутингом (стандартный `net/http` + `net/http/mux`, без внешних фреймворков)
- Пакет `internal/auth` — генерация/валидация JWT-токенов
- Middleware `CheckJWT` для защиты эндпоинтов
- Эндпоинты `/auth/login` и `/auth/logout`
- In-memory blacklist для инвалидации токенов
- Полное покрытие unit-тестами (≥80%)

**Библиотека:** `github.com/golang-jwt/jwt/v5`

## Подход: TDD

Каждая задача следует циклу Red → Green → Refactor:
1. Пишем тест
2. Запускаем — видим падение
3. Пишем минимальный код для прохождения
4. Запускаем — видим зелёный
5. Рефакторим

---

## Задачи

### 1. Установить зависимость `golang-jwt/jwt`
- [ ] Выполнить `go get github.com/golang-jwt/jwt/v5`
- [ ] Проверить что `go mod tidy` не ругается

### 2. Пакет `internal/auth` — генерация токенов

**2.1. RED: Тест `TestGenerateToken`**
- [ ] Создать `internal/auth/auth_test.go`
- [ ] Написать тест: вызов `GenerateToken(username, secret)` возвращает непустую строку и парсится обратно тем же секретом
- [ ] Запустить тест — убедиться что он падает (пакет не существует)

**2.2. GREEN: Минимальная реализация**
- [ ] Создать `internal/auth/auth.go`
- [ ] Определить структуру `Claims` с `Username` и стандартными полями (`RegisteredClaims` из jwt/v5)
- [ ] Реализовать `GenerateToken(username, secret string) (string, error)` — создаёт токен с `Claims`, подписывает HMAC-SHA256
- [ ] Запустить тест — убедиться что он проходит

**2.3. RED: Тест `TestParseToken`**
- [ ] Написать тест: `ParseToken(validToken, secret)` возвращает корректные claims
- [ ] Написать тест: `ParseToken(expiredToken, secret)` возвращает ошибку
- [ ] Написать тест: `ParseToken(token, wrongSecret)` возвращает ошибку
- [ ] Запустить — убедиться что падает

**2.4. GREEN: Реализация `ParseToken`**
- [ ] Реализовать `ParseToken(tokenString, secret string) (*Claims, error)` — парсит и валидирует токен (expiration, подпись)
- [ ] Запустить — убедиться что все тесты проходят

### 3. In-memory token blacklist

**3.1. RED: Тесты blacklist**
- [ ] Тест `TestBlacklist_AddAndCheck`: добавляем токен в blacklist, `IsBlacklisted` возвращает true
- [ ] Тест `TestBlacklist_NotListed`: `IsBlacklisted` для неизвестного токена возвращает false
- [ ] Тест `TestBlacklist_ExpiredCleanup`: добавляем токен с истёкшим TTL, после очистки `IsBlacklisted` возвращает false
- [ ] Запустить — убедиться что падает

**3.2. GREEN: Реализация `Blacklist`**
- [ ] Структура `Blacklist` с `sync.RWMutex` и `map[string]time.Time` (token → expiry)
- [ ] Метод `Add(token string, ttl time.Duration)`
- [ ] Метод `IsBlacklisted(token string) bool`
- [ ] Метод `CleanExpired()` — удаляет просроченные записи
- [ ] Запустить — все тесты проходят

### 4. HTTP middleware `CheckJWT`

**4.1. RED: Тесты middleware**
- [ ] Тест `TestCheckJWT_ValidToken`: запрос с валидным `Authorization: Bearer <token>` — next handler вызывается, username в context
- [ ] Тест `TestCheckJWT_MissingHeader`: запрос без заголовка → 401 Unauthorized
- [ ] Тест `TestCheckJWT_InvalidToken`: запрос с невалидным токеном → 401
- [ ] Тест `TestCheckJWT_BlacklistedToken`: запрос с токеном из blacklist → 401
- [ ] Запустить — падает

**4.2. GREEN: Реализация `CheckJWT`**
- [ ] Создать `internal/auth/middleware.go`
- [ ] Функция `CheckJWT(secret string, blacklist *Blacklist) func(http.Handler) http.Handler`
- [ ] Извлекает токен из `Authorization: Bearer <token>`
- [ ] Парсит через `ParseToken`, проверяет blacklist
- [ ] Кладёт `username` в `context.Context`
- [ ] При ошибке → `http.Error(w, "Unauthorized", http.StatusUnauthorized)`
- [ ] Запустить — все тесты проходят

### 5. Эндпоинт `POST /auth/login`

**5.1. RED: Тесты login**
- [ ] Тест `TestLogin_Success`: POST с верными credentials → 200 + JSON `{"token": "..."}`
- [ ] Тест `TestLogin_WrongPassword`: POST с неверным паролем → 401
- [ ] Тест `TestLogin_InvalidJSON`: POST с невалидным JSON → 400
- [ ] Тест `TestLogin_MissingFields`: POST без username/password → 400
- [ ] Запустить — падает

**5.2. GREEN: Реализация login handler**
- [ ] Создать `internal/auth/handler.go`
- [ ] Структура `LoginRequest` — `Username`, `Password` (JSON)
- [ ] Структура `LoginResponse` — `Token string` (JSON)
- [ ] Структура `UserStore` — интерфейс для проверки credentials (map пользователей в памяти для начала)
- [ ] `LoginHandler(userStore UserStore, secret string) http.HandlerFunc`
- [ ] Запустить — все тесты проходят

### 6. Эндпоинт `POST /auth/logout`

**6.1. RED: Тесты logout**
- [ ] Тест `TestLogout_Success`: POST с валидным Bearer токеном → 200, токен в blacklist
- [ ] Тест `TestLogout_NoToken`: POST без токена → 401
- [ ] Запустить — падает

**6.2. GREEN: Реализация logout handler**
- [ ] `LogoutHandler(secret string, blacklist *Blacklist) http.HandlerFunc`
- [ ] Извлекает токен, добавляет в blacklist с TTL равным оставшемуся времени жизни токена
- [ ] Запустить — все тесты проходят

### 7. Сборка роутера и HTTP-сервера

**7.1. RED: Тесты сервера**
- [ ] Тест `TestServer_LoginEndpoint`: проверяет что `/auth/login` доступен и отвечает
- [ ] Тест `TestServer_ProtectedEndpoint`: проверяет что защищённый эндпоинт требует токен
- [ ] Тест `TestServer_LogoutInvalidatesToken`: полный цикл login → access → logout → access denied
- [ ] Запустить — падает

**7.2. GREEN: Реализация**
- [ ] Создать `internal/auth/server.go`
- [ ] Функция `NewRouter(cfg Config) *http.ServeMux` — регистрирует маршруты
- [ ] Открытые маршруты: `POST /auth/login`, `POST /auth/logout`
- [ ] Защищённые маршруты (через `CheckJWT`): заглушка `/api/health` для демонстрации
- [ ] Запустить — все тесты проходят

### 8. Конфигурация auth

**8.1. Обновить `pkg/config/config.go`**
- [ ] Добавить секцию `AuthConfig` в `Config`: `Secret string`, `TokenTTL time.Duration`, `Users map[string]string` (username → password hash)
- [ ] Добавить значения по умолчанию в `Default()`

**8.2. RED: Тесты конфигурации**
- [ ] Тест что `AuthConfig` загружается из YAML
- [ ] Тест дефолтных значений

**8.3. GREEN: Реализация**
- [ ] Заполнить поля в `Default()` и `merge()`
- [ ] Запустить — все тесты проходят

### 9. Интеграционный тест полного цикла

- [ ] RED: Тест `TestIntegration_AuthFullCycle`:
  - Создать тестовый сервер с конфигурацией
  - Login → получить токен
  - Обратиться к защищённому эндпоинту с токеном → 200
  - Logout
  - Обратиться к защищённому эндпоинту с тем же токеном → 401
  - Запустить — падает
- [ ] GREEN: убедиться что всё работает вместе
- [ ] REFACTOR: вынести общие хелперы в `test_helpers.go`

### 10. Проверка качества

- [ ] Запустить `make lint` — без ошибок
- [ ] Запустить `go test ./internal/auth/... -cover` — покрытие ≥ 80%
- [ ] Запустить `go test ./...` — все тесты проекта проходят
- [ ] Проверить отсутствие `scaffold`-кода и TODO

---

## Структура файлов

```
internal/
└── auth/
    ├── auth.go           # GenerateToken, ParseToken, Claims, Blacklist
    ├── middleware.go      # CheckJWT middleware
    ├── handler.go         # LoginHandler, LogoutHandler, UserStore
    ├── server.go          # NewRouter, конфигурация сервера
    ├── auth_test.go       # Тесты генерации/парсинга токенов, blacklist
    ├── middleware_test.go  # Тесты middleware
    ├── handler_test.go    # Тесты login/logout handlers
    ├── server_test.go     # Интеграционные тесты сервера
    └── test_helpers.go    # Общие хелперы для тестов
```

## Зависимости

- `github.com/golang-jwt/jwt/v5` — JWT генерация/валидация
- `golang.org/x/crypto/bcrypt` — хеширование паролей (для UserStore)

## Дизайн-решения

1. **Без внешнего HTTP-фреймворка** — только `net/http` + `net/http/mux`, проще и меньше зависимостей
2. **In-memory blacklist** — `map[string]time.Time` + `sync.RWMutex`, достаточно для одного инстанса
3. **HMAC-SHA256** (HS256) — стандартный симметричный алгоритм, достаточный для большинства случаев
4. **UserStore как интерфейс** — позволяет менять реализацию (map → DB) без изменения handlers
5. **bcrypt для паролей** — стандарт хеширования паролей в Go
