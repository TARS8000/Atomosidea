# Use a minimal base image
FROM alpine:latest

# Install YARA
RUN apk add --no-cache yara

# Copy YARA rules from the build context into the image
# The build context is the project root, so rules are at ./security/yara-rules
COPY ./security/yara-rules /etc/yara-rules

# A simple command to verify the image can be built
CMD ["yara", "--version"]
