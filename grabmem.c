#include <unistd.h>
#include <stdlib.h>

int main(int argc, char **argv)
{
  sleep(1);
  int kb = atoi(argv[1]);
  for (int i = 0; i < kb; i++) {
    malloc(1024);
  }
  while (1) {
    sleep(3600);
  }
}
