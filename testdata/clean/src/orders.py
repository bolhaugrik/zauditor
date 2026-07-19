"""Order totals."""


def total(items: list[dict]) -> float:
    return sum(i["price"] * i["qty"] for i in items)
