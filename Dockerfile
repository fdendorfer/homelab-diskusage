FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
COPY static ./static
RUN CGO_ENABLED=0 go build -o /diskusage .

FROM scratch
COPY --from=build /diskusage /diskusage
EXPOSE 8080
ENTRYPOINT ["/diskusage"]
