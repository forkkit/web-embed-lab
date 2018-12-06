package ws

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/websocket"
)

/*
WebSocketConnection holds state for an incoming connection from a browser client
*/
type WebSocketConnection struct {
	ClientUUID string             // Assigned when the connection comes in
	Conn       *websocket.Conn    // The connection back to the client
	Outgoing   chan ClientMessage // A buffer for outgoing ClientMessages
	Stop       chan bool          // Send a bool to this to stop HandleOutgoing
}

/*
HandleOutgoing is called in a go routine to read outgoing messages from Outgoing and send it out via Conn
Sending any bool over Stop will break out of the routine
*/
func (wsConn *WebSocketConnection) HandleOutgoing() {
	for {
		select {
		case clientMessage := <-wsConn.Outgoing:
			rawResponse, err := json.Marshal(clientMessage)
			if err = wsConn.Conn.WriteMessage(1, rawResponse); err != nil {
				logger.Println(err)
			}
		case <-wsConn.Stop:
			return
		}
	}
}

/*
WebSocketHandler holds the state and logic for all WebSocket handling.
Includes a list of active connections
*/
type WebSocketHandler struct {
	Connections map[string]*WebSocketConnection
	Upgrader    websocket.Upgrader
}

func NewWebSocketHandler() *WebSocketHandler {
	wsHandler := &WebSocketHandler{
		Connections: make(map[string]*WebSocketConnection),
	}
	wsHandler.Upgrader = websocket.Upgrader{
		ReadBufferSize:  2048,
		WriteBufferSize: 2048,
		CheckOrigin: func(r *http.Request) bool {
			// TODO Actually check the WebSocket origin
			return true
		},
	}
	return wsHandler
}

/*
Distribute queues a ClientMessage for sending by WebSocket connections identified by client UUID
*/
func (handler *WebSocketHandler) Distribute(clientUUIDs []string, message ClientMessage) {
	for _, clientUUID := range clientUUIDs {
		_, ok := handler.Connections[clientUUID]
		if ok == false {
			continue
		}
		handler.Connections[clientUUID].Outgoing <- message
	}
}

func (handler *WebSocketHandler) AddWebSocketConnection(connection *WebSocketConnection) {
	handler.Connections[connection.ClientUUID] = connection
}

func (handler *WebSocketHandler) RemoveWebSocketConnection(connection *WebSocketConnection) {
	delete(handler.Connections, connection.ClientUUID)
}

/*
ServeHTTP is called by the HTTP service when a new client connection comes in.
It tries to upgrade the connection to WebSocket. If successful, it loops over incoming messages and sends them to RouteClientMessage.
*/
func (handler WebSocketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := handler.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Println("Error upgrading WebSocket connection", err)
		return
	}

	wsConnection := &WebSocketConnection{
		ClientUUID: UUID(),
		Conn:       conn,
		Outgoing:   make(chan ClientMessage, 2048),
		Stop:       make(chan bool),
	}
	handler.AddWebSocketConnection(wsConnection)

	go wsConnection.HandleOutgoing() // Sends outgoing messages from the wsConnection.Outgoing channel
	defer func() {
		handler.RemoveWebSocketConnection(wsConnection)
		conn.Close()
		wsConnection.Stop <- true // Stops HandleOutgoing go routine
		RouteClientMessage(NewClientDisconnectedMessage(), wsConnection.ClientUUID)
	}()

	// Send the initial Connect message
	wsConnection.Outgoing <- NewConnectedMessage(wsConnection.ClientUUID)

	for {
		// Read
		_, rawMessage, err := wsConnection.Conn.ReadMessage()
		if err != nil {
			break
		}

		// Parse
		typedMessage, err := ParseMessageJson(string(rawMessage))
		if err != nil {
			logger.Println("Could not parse ClientMessage", err, rawMessage)
			continue
		}

		// Debug log messages we just log and then ignore
		if typedMessage.MessageType() == DebugLogType {
			logger.Println("DEBUG LOG", string(rawMessage))
			continue
		}

		// Route
		clientUUIDs, responseMessage, err := RouteClientMessage(typedMessage, wsConnection.ClientUUID)
		if err != nil {
			logger.Printf("Error routing client message: %s", err)
		}
		// Respond
		if responseMessage != nil && len(clientUUIDs) > 0 {
			handler.Distribute(clientUUIDs, responseMessage)
		}
	}
}
