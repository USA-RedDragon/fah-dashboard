# fah-competition-dashboard

[![coverage](https://raw.githubusercontent.com/USA-RedDragon/fah-competition-dashboard/main/.github/badges/coverage.svg)](https://github.com/USA-RedDragon/fah-competition-dashboard/actions)

Simple service to log HTTP requests for debugging purposes.

## Configuration

Configuration is read from `config.yaml` in the working directory (or the file passed with `--config`), environment variables, and command-line flags. Copy [config.example.yaml](config.example.yaml) to `config.yaml` to get started.

<!-- configulator:begin -->

| Key         | Type    | Default | Environment | Flag          | Description                                                           |
|-------------|---------|---------|-------------|---------------|-----------------------------------------------------------------------|
| `log-level` | string  | `info`  | `LOG_LEVEL` | `--log-level` | Logging level for the application. One of debug, info, warn, or error |
| `http.bind` | string  | `[::]`  | `HTTP_BIND` | `--http.bind` | Address to listen on. The default, [::], listens on all interfaces    |
| `http.port` | integer | `8080`  | `HTTP_PORT` | `--http.port` | Port to listen on                                                     |

<!-- configulator:end -->
