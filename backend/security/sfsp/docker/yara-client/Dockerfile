# Use a minimal base image
FROM alpine:latest

# Install YARA
RUN apk add --no-cache yara

# Copy YARA rules from the build context into the image
# The build context is the project root, so rules are at ./backend/security/sfsp/yara-rules
COPY ./backend/security/sfsp/yara-rules /etc/yara-rules

# A simple command to verify the image can be built
CMD ["yara", "--version"]
