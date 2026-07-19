from src.orders import total


def test_total():
    assert total([{"price": 2.0, "qty": 3}]) == 6.0
