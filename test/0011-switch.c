
int x = 0;

int main() {

	switch(x)
		case 0:
			;
	switch(x)
		case 1:
			return 1;
	switch(x) {
		{
			x = 1 + 1;
			foo:
			case 1:
				return 1;
		}
	}
	switch(x) {
		case 0:
			return x;
		case 1:
			return 1;
		default:
			return 1;
	}
}