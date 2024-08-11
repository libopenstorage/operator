package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	pb "github.com/libopenstorage/operator/proto"

	"google.golang.org/grpc"
)

var host = "localhost"
var address = fmt.Sprintf("%v:50051", host)

func main() {
	conn, err := grpc.Dial(address, grpc.WithInsecure())
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()
	client := pb.NewSemaphoreServiceClient(conn)

	workStartTime := time.Now()
	wg := sync.WaitGroup{}
	resourceId := "resource-x"

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			clientId := fmt.Sprintf("client-%v", i)
			// poll acquire until locked
			for {
				req := &pb.AcquireLockRequest{
					ResourceId:     resourceId,
					ClientId:       clientId,
					AccessPriority: pb.SemaphoreAccessPriority_LOW,
				}
				resp, err := client.AcquireLock(context.Background(), req)
				if err != nil {
					fmt.Printf("Acquire Error: %v\n", err)
				} else {
					if resp.GetAccessStatus() == pb.SemaphoreAccessStatus_LOCKED {
						fmt.Printf("Acquire Response for %v: %v\n", clientId, resp)
						break
					}
				}
			}
			// poll until successfull release
			for {
				req := &pb.ReleaseLockRequest{
					ResourceId: resourceId,
					ClientId:   clientId,
				}
				_, err := client.ReleaseLock(context.Background(), req)
				if err == nil {
					break
				}
				fmt.Printf("Release Error: %v\n", err)
			}
		}(i)
	}

	wg.Wait()
	fmt.Println("Total time: ", time.Since(workStartTime))
}
