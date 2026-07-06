# gorilla/websocket (github.com/gorilla/websocket)

WebSocket-сервер для стриминга событий дашборда клиенту. Аудитория: клеточка `pkg/server`.

## Апгрейд HTTP-соединения и стриминг событий

`websocket.Upgrader` создаётся один раз на пакет (не на запрос). `CheckOrigin` разрешён из любого
источника (для локального дашборда). Обработчик апгрейдит соединение, подписывается на шину событий и
пишет каждое событие как JSON-текстовое сообщение, пока канал подписки открыт.

```go
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade: %v", err)
		return
	}
	defer conn.Close()

	id, ch := s.uiBus.Subscribe(64)
	defer s.uiBus.Unsubscribe(id)

	for ev := range ch {
		data, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return // клиент отключился/ошибка записи — завершить горутину
		}
	}
}
```

## Управляемое закрытие при переполнении очереди

Когда подписчик не успевает вычитывать события и шина начинает их дропать, соединение закрывается
явным close-фреймом с кодом 1008 (policy violation) вместо тихого разрыва:

```go
if drops := s.uiBus.SubscriberDroppedCount(id); drops > prevDrops {
	prevDrops = drops
	if drops > 10 {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(1008, "event queue overflow"))
		return
	}
}
```

## Особенности

- Запись в соединение (`WriteMessage`) выполняется только из горутины-обработчика этого соединения —
  без дополнительной синхронизации, т.к. `*websocket.Conn` не потокобезопасен для конкурентных `Write`.
- Ошибка `WriteMessage` трактуется как обрыв соединения — обработчик завершается, `defer conn.Close()`
  освобождает ресурс.
