<?php
declare(strict_types=1);

/** Returns the adapter readiness state. */
function health(): array
{
    return ['status' => 'ok'];
}

/** Returns a normalized synthetic payload placeholder for the ingestion boundary. */
function normalizeLegacyPayload(array $payload): array
{
    return ['source' => 'legacy', 'payload' => $payload];
}

header('Content-Type: application/json');
if ($_SERVER['REQUEST_URI'] === '/health') {
    echo json_encode(health(), JSON_THROW_ON_ERROR);
    exit;
}
if ($_SERVER['REQUEST_METHOD'] === 'POST') {
    $body = file_get_contents('php://input');
    $payload = json_decode($body ?: '{}', true, 512, JSON_THROW_ON_ERROR);
    echo json_encode(normalizeLegacyPayload($payload), JSON_THROW_ON_ERROR);
    exit;
}
http_response_code(404);
echo json_encode(['error' => 'not found'], JSON_THROW_ON_ERROR);
