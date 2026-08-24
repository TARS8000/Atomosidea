# Use the official ClamAV image as a base
FROM clamav/clamav:latest

# The purpose of this image is to provide the clamscan utility.
# The entrypoint will be overridden by the sandbox runner.
# We just need to ensure the virus definitions are up to date.
# The base image already handles this with freshclam.

# A simple command to verify the image can be built
CMD ["clamscan", "--version"]
