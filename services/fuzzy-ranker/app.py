"""Expose advisory candidate ranking without returning financial decisions."""

from fastapi import FastAPI
from pydantic import BaseModel

app = FastAPI(title="SettleTrace advisory ranker")


class Candidate(BaseModel):
    """Represent one narrowed settlement candidate."""

    id: str
    credit_paise: int


class RankRequest(BaseModel):
    """Represent a payment amount and its pre-narrowed candidates."""

    expected_net_paise: int
    candidates: list[Candidate]


def rank_candidates(request: RankRequest) -> list[dict]:
    """Rank candidates by absolute amount proximity without declaring a match."""
    return [
        {
            "id": candidate.id,
            "score": 1.0 / (1.0 + abs(candidate.credit_paise - request.expected_net_paise)),
            "features": {"amount_distance_paise": abs(candidate.credit_paise - request.expected_net_paise)},
        }
        for candidate in sorted(request.candidates, key=lambda item: abs(item.credit_paise - request.expected_net_paise))
    ]


@app.get("/health")
def health() -> dict[str, str]:
    """Return the service readiness state."""
    return {"status": "ok"}


@app.post("/rank")
def rank(request: RankRequest) -> list[dict]:
    """Return advisory ranked candidates with no decision field."""
    return rank_candidates(request)
