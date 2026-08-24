# Use a minimal base image
FROM alpine:latest

# Install YARA
RUN apk add --no-cache yara

# The entrypoint will be overridden by the sandbox runner.
# The YARA rules will be mounted into the container by the sandbox.

# A simple command to verify the image can be built
CMD ["yara", "--version"]
