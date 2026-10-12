FROM golang:1.27 AS build

WORKDIR /app

COPY go.* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /bin/vyrr .
RUN mkdir -p /runtime-data/assets /runtime-data/tmp

FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=build --chown=nonroot:nonroot /runtime-data/ /app/data
WORKDIR /app

COPY --from=build /bin/vyrr /app/vyrr
COPY config.yml /app/config.yml

EXPOSE 9743
EXPOSE 9884

ENTRYPOINT ["/app/vyrr"]
