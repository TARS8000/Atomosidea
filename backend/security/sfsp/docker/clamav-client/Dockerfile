# Use the official ClamAV image as a base
FROM clamav/clamav:latest

# Ensure clamscan can write its temporary files/sockets
# ClamAV often tries to write to /var/clamav or /tmp
# We will use /tmp for temporary data and ensure it's writable.

# Create a writable directory for ClamAV's temporary files if needed
RUN mkdir -p /tmp/clamav_tmp && chmod 777 /tmp/clamav_tmp

# A simple command to verify the image can be built
# We will override the ENTRYPOINT/CMD in the sandbox to run clamscan with specific options
CMD ["clamscan", "--version"]
