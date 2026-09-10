package client_test

import (
	"context"
	"fmt"
	"time"

	"github.com/TamerlanK/hearth/pkg/client"
	"github.com/TamerlanK/hearth/pkg/protocol"
)

func ExampleDial() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := client.Dial(ctx, "localhost:4000", client.Options{Name: "bot", Room: "ops", Reconnect: true})
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	defer c.Close()

	if err := c.Say(ctx, "hello from Go"); err != nil {
		fmt.Println("say:", err)
		return
	}
	for e := range c.Events() {
		if e.Kind == protocol.Msg {
			fmt.Printf("%s: %s\n", e.From, e.Text)
		}
	}
}

func ExampleClient_Who() {
	ctx := context.Background()
	c, err := client.Dial(ctx, "localhost:4000", client.Options{Name: "bot"})
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	defer c.Close()

	names, err := c.Who(ctx)
	if err != nil {
		fmt.Println("who:", err)
		return
	}
	fmt.Println(names)
}
