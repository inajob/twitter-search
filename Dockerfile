#FROM golang:1.7.3
#WORKDIR /go/src/github.com/inajob/twitter-search/
#RUN go get -d -v github.com/dghubble/go-twitter/twitter
#RUN go get -d -v github.com/dghubble/oauth1
#RUN go get -d -v github.com/gin-gonic/gin
#RUN go get -d -v gopkg.in/olahol/melody.v1
#COPY main.go .
#RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o server .


FROM alpine
RUN apk --no-cache add ca-certificates
ADD server server
EXPOSE 8080
CMD ["/server"]

