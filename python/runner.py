import os
import subprocess
import sys

# Debug: List all files in the current directory
print("Files in current directory:")
for f in os.listdir('.'):
    print(f)

# Path to the Go binary (change as needed)
binary_path = "app-enidu"

# Make the binary executable
try:
    os.chmod(binary_path, 0o755)
except Exception as e:
    print(f"chmod failed: {e}")

# Run the binary and live print output
try:
    with subprocess.Popen([binary_path], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, bufsize=1, text=True) as proc:
        for line in proc.stdout:
            print(line, end='')
except Exception as e:
    print(f"Execution failed: {e}")