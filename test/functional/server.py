from flask import Flask
import time
import os

app = Flask(__name__)

@app.route("/")
def hello():
    latency = int(os.getenv("LATENCY", "10"))  # Default 10ms
    time.sleep(latency / 1000)  # Convert ms to seconds
    return f"Response from {os.getenv('APP_NAME', 'unknown')} with latency {latency}ms\n"

if __name__ == "__main__":
    app.run(host="0.0.0.0", port=8080)

